package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/tenant"
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

		// Data API routes (tenant-scoped via middleware).
		queryEngine := query.NewQueryEngine(pool)
		r.Route("/db", func(r chi.Router) {
			r.Use(tenant.TenantContextMiddleware(pool))
			r.Mount("/rpc", query.HandleRPC(queryEngine))
			r.Mount("/", query.HandleQuery(queryEngine))
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
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}
		ctx := auth.ContextWithClaims(r.Context(), &auth.Claims{
			Subject: "postman-test-user-001",
			Email:   "dev@eurobase.eu",
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
