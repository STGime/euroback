package functions

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// unforwardableHeaders are caller request headers the gateway must NOT
// forward to the runner (#214): hop-by-hop headers, the platform's auth
// credentials (consumed by the gateway, not the function), and Host/
// Content-Length (managed by the HTTP client). The gateway's own control
// headers (X-Eurobase-*, X-Project-*, X-Function-*, X-Schema-Name, X-Plan,
// X-User-*, X-Request-ID) are denied via isGatewayControlHeader so a caller
// can't spoof a signed identity value — those are set by the gateway after
// copying and are covered by the HMAC signature.
var unforwardableHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"host":                true,
	"content-length":      true,
	// Platform auth — these authenticate the caller to Eurobase and must
	// not leak to user function code.
	"authorization": true,
	"apikey":        true,
	"cookie":        true,
}

// isGatewayControlHeader reports whether a header is part of the
// gateway→runner control namespace that the gateway sets itself (and that
// the HMAC signature covers). A caller-supplied value must never be
// forwarded, or it could override a signed identity header / break
// verification (e.g. a forwarded X-Request-ID is signed empty by the
// gateway).
func isGatewayControlHeader(lower string) bool {
	if strings.HasPrefix(lower, "x-eurobase-") {
		return true
	}
	switch lower {
	case "x-project-id", "x-project-slug", "x-schema-name", "x-plan",
		"x-function-id", "x-function-name", "x-function-version",
		"x-user-id", "x-user-email", "x-request-id":
		return true
	}
	return false
}

// copyForwardableHeaders copies the caller's request headers that are safe
// to forward to the runner into dst (#214) — everything except hop-by-hop
// headers, platform auth credentials, and the gateway control namespace.
// Also drops any header named in the request's Connection header (RFC 7230
// §6.1 — those are connection-specific and a proxy must not forward them).
func copyForwardableHeaders(dst, src http.Header) {
	connectionTokens := map[string]bool{}
	for _, c := range src.Values("Connection") {
		for _, tok := range strings.Split(c, ",") {
			if t := strings.ToLower(strings.TrimSpace(tok)); t != "" {
				connectionTokens[t] = true
			}
		}
	}
	for name, values := range src {
		lower := strings.ToLower(name)
		if unforwardableHeaders[lower] || isGatewayControlHeader(lower) || connectionTokens[lower] {
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

// forwardableQuery returns the caller's query string with auth-bearing
// params removed, so a secret key passed as ?apikey=… (accepted by the
// API-key middleware, internal/auth/apikey_middleware.go) does not leak
// into the function's req.url (#214). Mirrors the header strip. Returns ""
// when nothing remains. Note: re-encoding re-sorts the params, which is
// fine for searchParams consumers.
func forwardableQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		// Unparseable query — forward nothing rather than risk leaking a
		// param the gateway consumes.
		return ""
	}
	q.Del("apikey")
	return q.Encode()
}

// HandleInvoke proxies a function invocation to the Function Runner service.
// If no runner is configured, it returns 501 Not Implemented.
//
// signer (optional) HMAC-signs the identity headers before forwarding so
// the runner can authenticate the request as having come from a real
// gateway and not a cluster-internal forger. Closes layer 3 of advisory
// GHSA-7428-mvpp-rhr7. Pass nil during the rollout window where the
// runner is in soft-mode (warn-only) — gateway pods without the secret
// can keep working until the secret + corresponding env var land in
// every environment.
func HandleInvoke(pool *pgxpool.Pool, svc *Service, runnerURL string, signer *Signer) http.HandlerFunc {
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

		// Proxy request to the Function Runner. Preserve the query string so
		// functions can read new URL(req.url).searchParams (#214), minus
		// auth-bearing params (?apikey=) that the gateway consumes.
		target := runnerURL + "/invoke"
		if q := forwardableQuery(r.URL.RawQuery); q != "" {
			target += "?" + q
		}
		proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			slog.Error("failed to create proxy request", "error", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Forward the caller's request headers so functions can read custom
		// headers — webhooks/APIs auth and route via X-Signature, X-Api-Key,
		// X-Webhook-*, etc. (#214). Skips hop-by-hop, platform auth, and the
		// gateway control namespace (set below; the HMAC covers it).
		copyForwardableHeaders(proxyReq.Header, r.Header)

		// Pass context as headers (set AFTER copying so they always win).
		proxyReq.Header.Set("X-Project-ID", projectCtx.ProjectID)
		proxyReq.Header.Set("X-Schema-Name", projectCtx.SchemaName)
		proxyReq.Header.Set("X-Function-Name", functionName)
		proxyReq.Header.Set("X-Function-ID", fn.ID)
		// The runner keys its code cache on id+version, so a redeploy
		// (version bump) takes effect immediately instead of after the
		// cache TTL (closes #200). Not HMAC-covered: a forged version
		// can only cause a cache miss — code is always loaded from the
		// DB by id.
		proxyReq.Header.Set("X-Function-Version", strconv.Itoa(fn.Version))
		proxyReq.Header.Set("X-Plan", projectCtx.Plan)
		// Content-Type is already forwarded by copyForwardableHeaders above.

		if claims, ok := auth.EndUserClaimsFromContext(r.Context()); ok && claims != nil {
			proxyReq.Header.Set("X-User-ID", claims.UserID)
			proxyReq.Header.Set("X-User-Email", claims.Email)
		}

		// Sign the identity headers AFTER they're all set. Order
		// matters: the signature has to cover the final values.
		if signer != nil {
			signer.Sign(proxyReq.Header, time.Now())
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
