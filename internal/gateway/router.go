package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/enduser"
	"github.com/eurobase/euroback/internal/plans"
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

// NewRouter creates and configures the chi router.
//
// When devMode is true, the platform auth middleware is replaced with a
// pass-through that injects a fixed test user (for local curl/Postman testing).
// devMode must NEVER be enabled in production.
func NewRouter(pool *pgxpool.Pool, platformAuth *auth.PlatformAuthMiddleware, platformAuthSvc *auth.PlatformAuthService, limiter *ratelimit.RateLimiter, s3Client *storage.S3Client, hub *realtime.Hub, logCh chan<- LogEntry, subdomainMw *auth.SubdomainMiddleware, emailService *email.EmailService, limitsSvc *plans.LimitsService, devMode ...bool) chi.Router {
	r := chi.NewRouter()

	// Global middleware.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(CORSMiddleware)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Subdomain resolution — resolves {slug}.eurobase.app to a project context.
	if subdomainMw != nil {
		r.Use(subdomainMw.Handler)
	}

	isDev := len(devMode) > 0 && devMode[0]

	// Health check (unauthenticated).
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Tenant service.
	tenantSvc := tenant.NewTenantService(pool)

	// End-user auth service.
	endUserAuthSvc := enduser.NewAuthService(pool)
	if emailService != nil {
		endUserAuthSvc.SetEmailService(emailService)
	}

	// API key middleware (for SDK / end-user routes).
	apiKeyMw := auth.NewAPIKeyMiddleware(pool)

	// End-user JWT middleware (optional — anonymous if no token).
	endUserMw := auth.NewEndUserMiddleware()

	// ── Platform routes ──
	r.Route("/platform", func(r chi.Router) {
		// Unauthenticated: platform auth endpoints.
		r.Post("/auth/signup", auth.HandlePlatformSignUp(platformAuthSvc))
		r.Post("/auth/signin", auth.HandlePlatformSignIn(platformAuthSvc))
		r.Post("/auth/forgot-password", auth.HandlePlatformForgotPassword(platformAuthSvc))
		r.Post("/auth/reset-password", auth.HandlePlatformResetPassword(platformAuthSvc))

		// Authenticated: account management.
		r.Route("/auth/account", func(r chi.Router) {
			if isDev {
				r.Use(devAuthMiddleware)
			} else {
				r.Use(platformAuth.Handler)
			}
			r.Get("/profile", auth.HandleGetProfile(platformAuthSvc))
			r.Patch("/profile", auth.HandleUpdateProfile(platformAuthSvc))
			r.Post("/change-password", auth.HandleChangePassword(platformAuthSvc))
			r.Post("/delete", auth.HandleDeleteAccount(platformAuthSvc))
		})

		// Authenticated: platform config endpoints.
		r.Route("/config", func(r chi.Router) {
			if isDev {
				r.Use(devAuthMiddleware)
			} else {
				r.Use(platformAuth.Handler)
			}
			if emailService != nil {
				r.Get("/email-status", email.HandleEmailStatus(emailService))
			} else {
				r.Get("/email-status", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]bool{"configured": false})
				})
			}
			if limitsSvc != nil {
				r.Get("/plans", plans.HandleGetPlans(limitsSvc))
			}
		})

		// Authenticated: project management & schema introspection.
		r.Route("/projects/{id}", func(r chi.Router) {
			if isDev {
				r.Use(devAuthMiddleware)
			} else {
				r.Use(platformAuth.Handler)
			}
			if logCh != nil {
				r.Use(RequestLoggingMiddleware(logCh))
			}
			r.Get("/logs", HandleLogs(pool))
			r.Get("/schema", query.HandleSchemaIntrospection(pool))
			r.Get("/schema/changes", query.HandleSchemaChanges(pool))
			r.Mount("/schema/tables", query.HandleDDL(pool))
			r.Mount("/webhooks", webhook.Routes(pool, limitsSvc))
			cronSvc := cron.NewCronService(pool)
			r.Mount("/cron", cron.Routes(cronSvc))
			r.Get("/api-keys", tenant.HandleListAPIKeys(pool))
			r.Post("/api-keys/regenerate", tenant.HandleRegenerateAPIKeys(pool))
			r.Get("/connect", tenant.HandleConnect(pool))

			// Plan usage.
			if limitsSvc != nil {
				r.Get("/usage", plans.HandleGetUsage(limitsSvc, pool))
			}

			// Email template management.
			if emailService != nil {
				tmplHandler := email.NewTemplateHandler(pool, emailService, limitsSvc)
				r.Get("/email-templates", tmplHandler.HandleList())
				r.Put("/email-templates/{type}", tmplHandler.HandleUpdate())
				r.Delete("/email-templates/{type}", tmplHandler.HandleDelete())
				r.Post("/email-templates/{type}/preview", tmplHandler.HandlePreview())
				r.Post("/email-templates/{type}/test", tmplHandler.HandleTest())
			}

			// Console end-user management — platform-authenticated.
			r.Route("/users", func(r chi.Router) {
				r.Use(tenant.PlatformTenantContext(pool))
				r.Mount("/", enduser.PlatformRoutes(pool))
			})

			// Console storage proxy — platform-authenticated access to project storage.
			if s3Client != nil {
				r.Route("/storage", func(r chi.Router) {
					r.Use(tenant.PlatformStorageContext(pool))
					storageHandler := storage.NewStorageHandler(s3Client)
					r.Mount("/", storageHandler.Routes())
				})
			}

			// Console data proxy — platform-authenticated access to project data.
			// Note: {id} here shadows the outer {id} (project ID) which is fine —
			// PlatformTenantContext already resolved the project in middleware.
			r.Route("/data", func(r chi.Router) {
				r.Use(tenant.PlatformTenantContext(pool))

				queryEngine := query.NewQueryEngine(pool)
				publisher := realtime.NewEventPublisher(nil, hub)

				r.Post("/sql", query.HandleSQL(queryEngine))
				r.Get("/{table}", query.HandleTableGet(queryEngine))
				r.Get("/{table}/{id}", query.HandleTableGetByID(queryEngine))
				r.Post("/{table}", query.HandleTableInsert(queryEngine, publisher))
				r.Post("/{table}/bulk-delete", query.HandleTableBulkDelete(queryEngine, publisher))
				r.Patch("/{table}/{id}", query.HandleTableUpdate(queryEngine, publisher))
				r.Delete("/{table}/{id}", query.HandleTableDelete(queryEngine, publisher))
			})
		})
	})

	// ── Tenant management routes (platform-authenticated) ──
	r.Route("/v1/tenants", func(r chi.Router) {
		if isDev {
			slog.Warn("DEV MODE ENABLED — auth middleware bypassed with test user")
			r.Use(devAuthMiddleware)
		} else {
			r.Use(platformAuth.Handler)
		}

		r.Post("/", tenant.HandleCreateProject(pool, tenantSvc, limitsSvc))
		r.Get("/", tenant.HandleListProjects(pool, tenantSvc))
		r.Patch("/{id}", tenant.HandleUpdateProject(pool, tenantSvc))
		r.Delete("/{id}", tenant.HandleDeleteProject(pool, tenantSvc))
	})

	// ── WebSocket realtime route ──
	if hub != nil {
		var tokenValidator func(token string) (string, error)
		if !isDev {
			tokenValidator = platformAuth.ValidateToken
		}
		wsHandler := realtime.HandleWebSocket(hub, tokenValidator, buildTenantResolver(pool), isDev)
		r.Get("/v1/realtime", wsHandler)
	} else {
		slog.Warn("realtime hub not configured, websocket route disabled")
	}

	// ── SDK routes (API key authenticated) ──
	r.Route("/v1", func(r chi.Router) {
		// Auth endpoints (only need API key, no end-user JWT).
		r.Route("/auth", func(r chi.Router) {
			r.Use(apiKeyMw.Handler)
			r.Post("/signup", enduser.HandleSignUp(endUserAuthSvc))
			r.Post("/signin", enduser.HandleSignIn(endUserAuthSvc))
			r.Post("/refresh", enduser.HandleRefresh(endUserAuthSvc))
			r.Post("/signout", enduser.HandleSignOut(endUserAuthSvc))
			r.Post("/forgot-password", enduser.HandleForgotPassword(endUserAuthSvc))
			r.Post("/reset-password", enduser.HandleResetPassword(endUserAuthSvc))
			r.Post("/verify-email", enduser.HandleVerifyEmail(endUserAuthSvc))
			r.Post("/resend-verification", enduser.HandleResendVerification(endUserAuthSvc))
			r.Post("/request-magic-link", enduser.HandleRequestMagicLink(endUserAuthSvc))
			r.Post("/signin-magic-link", enduser.HandleSignInWithMagicLink(endUserAuthSvc))

			// GET /v1/auth/user requires end-user JWT.
			r.Group(func(r chi.Router) {
				r.Use(endUserMw.Handler)
				r.Get("/user", enduser.HandleGetUser(endUserAuthSvc))
			})
		})

		// Data API routes (API key + optional end-user JWT).
		r.Route("/db", func(r chi.Router) {
			if isDev {
				r.Use(devAuthMiddleware)
				r.Use(tenant.TenantContextMiddleware(pool))
			} else {
				r.Use(apiKeyMw.Handler)
				r.Use(endUserMw.Handler)
				r.Use(tenant.TenantContextFromProject())
			}

			// Rate limiting.
			if limiter != nil {
				r.Use(ratelimit.RateLimitMiddleware(limiter))
			}

			queryEngine := query.NewQueryEngine(pool)
			publisher := realtime.NewEventPublisher(nil, hub)

			r.Post("/sql", query.HandleSQL(queryEngine))
			r.Mount("/rpc", query.HandleRPC(queryEngine))
			r.Get("/{table}", query.HandleTableGet(queryEngine))
			r.Get("/{table}/{id}", query.HandleTableGetByID(queryEngine))
			r.Post("/{table}", query.HandleTableInsert(queryEngine, publisher))
			r.Post("/{table}/bulk-delete", query.HandleTableBulkDelete(queryEngine, publisher))
			r.Patch("/{table}/{id}", query.HandleTableUpdate(queryEngine, publisher))
			r.Delete("/{table}/{id}", query.HandleTableDelete(queryEngine, publisher))
		})

		// Storage routes (API key + optional end-user JWT).
		if s3Client != nil {
			r.Route("/storage", func(r chi.Router) {
				if isDev {
					r.Use(devAuthMiddleware)
				} else {
					r.Use(apiKeyMw.Handler)
					r.Use(endUserMw.Handler)
				}

				storageHandler := storage.NewStorageHandler(s3Client)
				r.Mount("/", storageHandler.Routes())
			})
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
			 WHERE u.id = $1 AND p.status = 'active'
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

		subject := "00000000-0000-0000-0000-000000000001"
		email := "dev@eurobase.eu"

		ctx := auth.ContextWithClaims(r.Context(), &auth.Claims{
			Subject: subject,
			Email:   email,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
