package functions

import (
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HandleInvoke proxies a function invocation to the Function Runner service.
// If no runner is configured, it returns 501 Not Implemented.
func HandleInvoke(pool *pgxpool.Pool, svc *Service, runnerURL string) http.HandlerFunc {
	client := &http.Client{Timeout: 65 * time.Second} // slightly above max function timeout

	return func(w http.ResponseWriter, r *http.Request) {
		functionName := chi.URLParam(r, "name")

		// Resolve project from the API key context.
		projectCtx, ok := auth.ProjectFromContext(r.Context())
		if !ok || projectCtx == nil {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}

		// Look up the function.
		fn, err := svc.Get(r.Context(), projectCtx.ProjectID, functionName)
		if err != nil {
			jsonError(w, "function not found", http.StatusNotFound)
			return
		}

		if fn.Status != "active" {
			jsonError(w, "function is disabled", http.StatusServiceUnavailable)
			return
		}

		// Check JWT requirement.
		if fn.VerifyJWT {
			claims, hasClaims := auth.EndUserClaimsFromContext(r.Context())
			if !hasClaims || claims == nil {
				jsonError(w, "function requires authentication", http.StatusUnauthorized)
				return
			}
		}

		start := time.Now()

		if runnerURL == "" {
			// No runner configured — log and return 501.
			svc.LogInvocation(r.Context(), fn.ID, projectCtx.ProjectID, 501, 0, "function runner not configured", r.Method)
			jsonError(w, "edge functions runtime not available", http.StatusNotImplemented)
			return
		}

		// Proxy request to the Function Runner.
		proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, runnerURL+"/invoke", r.Body)
		if err != nil {
			slog.Error("failed to create proxy request", "error", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Pass context as headers.
		proxyReq.Header.Set("X-Project-ID", projectCtx.ProjectID)
		proxyReq.Header.Set("X-Schema-Name", projectCtx.SchemaName)
		proxyReq.Header.Set("X-Function-Name", functionName)
		proxyReq.Header.Set("X-Function-ID", fn.ID)
		proxyReq.Header.Set("X-Plan", projectCtx.Plan)
		proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

		if claims, ok := auth.EndUserClaimsFromContext(r.Context()); ok && claims != nil {
			proxyReq.Header.Set("X-User-ID", claims.UserID)
			proxyReq.Header.Set("X-User-Email", claims.Email)
		}

		resp, err := client.Do(proxyReq)
		if err != nil {
			durationMs := int(time.Since(start).Milliseconds())
			svc.LogInvocation(r.Context(), fn.ID, projectCtx.ProjectID, 502, durationMs, err.Error(), r.Method)
			slog.Error("function runner request failed", "function", functionName, "error", err)
			jsonError(w, "function execution failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		durationMs := int(time.Since(start).Milliseconds())

		// Log the invocation.
		if resp.StatusCode >= 500 {
			body, _ := io.ReadAll(resp.Body)
			errMsg := string(body)
			svc.LogInvocation(r.Context(), fn.ID, projectCtx.ProjectID, resp.StatusCode, durationMs, errMsg, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(body)
			return
		}

		svc.LogInvocation(r.Context(), fn.ID, projectCtx.ProjectID, resp.StatusCode, durationMs, "", r.Method)

		// Forward response.
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}
