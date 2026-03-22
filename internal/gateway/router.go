package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/eurobase/euroback/internal/webhook"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewRouter creates and returns a fully configured chi router with all
// platform and API routes. This is extracted from main.go to allow
// integration testing with httptest.
// NewRouter creates and configures the chi router. When devMode is true, the
// Hanko JWT middleware is replaced with a pass-through that injects a fixed
// test user, allowing local testing with curl/Postman without a running Hanko
// instance. devMode must NEVER be enabled in production.
func NewRouter(pool *pgxpool.Pool, hankoAuth *auth.HankoMiddleware, hankoWebhookSecret string, limiter *ratelimit.RateLimiter, s3Client *storage.S3Client, hub *realtime.Hub, devMode ...bool) chi.Router {
	r := chi.NewRouter()

	// Global middleware.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(CORSMiddleware)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Health check (unauthenticated).
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Tenant service.
	tenantSvc := tenant.NewTenantService(pool)

	// Platform routes.
	r.Route("/platform", func(r chi.Router) {
		r.Post("/webhooks/hanko", HankoWebhookHandler(pool, hankoWebhookSecret))

		// Schema introspection (authenticated).
		r.Route("/projects/{id}", func(r chi.Router) {
			if len(devMode) > 0 && devMode[0] {
				r.Use(devAuthMiddleware)
			} else {
				r.Use(hankoAuth.Handler)
			}
			r.Get("/schema", query.HandleSchemaIntrospection(pool))
			r.Get("/schema/changes", query.HandleSchemaChanges(pool))
			r.Mount("/schema/tables", query.HandleDDL(pool))
			r.Mount("/webhooks", webhook.Routes(pool))
			r.Get("/api-keys", tenant.HandleListAPIKeys(pool))
			r.Post("/api-keys/regenerate", tenant.HandleRegenerateAPIKeys(pool))
			r.Get("/connect", tenant.HandleConnect(pool))
		})
	})

	// WebSocket realtime route — auth is handled inside the handler via
	// query parameter, so this is mounted outside the standard auth middleware.
	if hub != nil {
		isDev := len(devMode) > 0 && devMode[0]

		// Build a token validator from the Hanko middleware.
		var tokenValidator func(token string) (string, error)
		if !isDev {
			tokenValidator = hankoAuth.ValidateToken
		}

		wsHandler := realtime.HandleWebSocket(hub, tokenValidator, buildTenantResolver(pool), isDev)
		r.Get("/v1/realtime", wsHandler)
	} else {
		slog.Warn("realtime hub not configured, websocket route disabled")
	}

	// V1 API routes (authenticated).
	r.Route("/v1", func(r chi.Router) {
		if len(devMode) > 0 && devMode[0] {
			slog.Warn("DEV MODE ENABLED — auth middleware bypassed with test user")
			r.Use(devAuthMiddleware)
		} else {
			r.Use(hankoAuth.Handler)
		}

		// Rate limiting (after auth so we have the tenant identity).
		if limiter != nil {
			r.Use(ratelimit.RateLimitMiddleware(limiter))
		} else {
			slog.Warn("rate limiter not configured, rate limiting disabled")
		}

		r.Post("/tenants", tenant.HandleCreateProject(pool, tenantSvc))
		r.Get("/tenants", tenant.HandleListProjects(pool, tenantSvc))
		r.Delete("/tenants/{id}", tenant.HandleDeleteProject(pool, tenantSvc))

		// Data API routes (tenant-scoped via middleware).
		queryEngine := query.NewQueryEngine(pool)
		publisher := realtime.NewEventPublisher(nil, hub)
		r.Route("/db", func(r chi.Router) {
			r.Use(tenant.TenantContextMiddleware(pool))
			r.Post("/sql", query.HandleSQL(queryEngine))
			r.Mount("/rpc", query.HandleRPC(queryEngine))
			// Catch-all table routes — must use explicit patterns so they
			// don't shadow /sql and /rpc when mounted at "/".
			r.Get("/{table}", query.HandleTableGet(queryEngine))
			r.Get("/{table}/{id}", query.HandleTableGetByID(queryEngine))
			r.Post("/{table}", query.HandleTableInsert(queryEngine, publisher))
			r.Patch("/{table}/{id}", query.HandleTableUpdate(queryEngine, publisher))
			r.Delete("/{table}/{id}", query.HandleTableDelete(queryEngine, publisher))
		})

		// Storage routes.
		if s3Client != nil {
			storageHandler := storage.NewStorageHandler(s3Client)
			r.Mount("/storage", storageHandler.Routes())
		} else {
			slog.Warn("s3 client not configured, storage routes disabled")
		}
	})

	return r
}

// buildTenantResolver returns a realtime.TenantResolver that looks up the
// user's default project and plan from the database.
func buildTenantResolver(pool *pgxpool.Pool) realtime.TenantResolver {
	return func(ctx context.Context, subject string) (string, string, error) {
		var projectID, plan string
		err := pool.QueryRow(ctx,
			`SELECT p.id, COALESCE(p.plan, 'free')
			 FROM projects p
			 JOIN platform_users u ON p.owner_id = u.id
			 WHERE u.hanko_user_id = $1 AND p.status = 'active'
			 ORDER BY p.created_at ASC
			 LIMIT 1`,
			subject,
		).Scan(&projectID, &plan)
		if err != nil {
			return "", "", fmt.Errorf("resolve tenant for subject %s: %w", subject, err)
		}
		return projectID, plan, nil
	}
}

// devAuthMiddleware injects a hardcoded test user for local development.
// An Authorization header must still be present (any value works), so that
// "no auth" requests are correctly rejected with 401.
// This must NEVER be used in production.
func devAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}

		// In dev mode, derive identity from the token value so different
		// login emails produce different users. The console stores
		// "dev_<base64(email)>_<timestamp>" as the token.
		subject := "postman-test-user-001"
		email := "dev@eurobase.eu"

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if strings.HasPrefix(token, "dev_") {
			parts := strings.SplitN(token, "_", 3) // ["dev", base64email, timestamp]
			if len(parts) >= 2 {
				if decoded, err := base64Decode(parts[1]); err == nil && decoded != "" {
					email = decoded
					// Use the email as subject so each email is a distinct user.
					subject = "dev-" + decoded
				}
			}
		}

		ctx := auth.ContextWithClaims(r.Context(), &auth.Claims{
			Subject: subject,
			Email:   email,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// base64Decode is a helper for dev mode token parsing.
func base64Decode(s string) (string, error) {
	// The console uses btoa() which produces standard base64; try both padded and unpadded.
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(s)
	}
	return string(b), err
}
