package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/enduser"
	"github.com/eurobase/euroback/internal/functions"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/eurobase/euroback/internal/sms"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/eurobase/euroback/internal/vault"
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
func NewRouter(pool *pgxpool.Pool, platformAuth *auth.PlatformAuthMiddleware, platformAuthSvc *auth.PlatformAuthService, limiter *ratelimit.RateLimiter, s3Client *storage.S3Client, hub *realtime.Hub, logCh chan<- LogEntry, subdomainMw *auth.SubdomainMiddleware, emailService *email.EmailService, smsService *sms.Service, limitsSvc *plans.LimitsService, vaultSvc *vault.VaultService, fnRunnerURL string, allowedOrigins []string, devMode ...bool) chi.Router {
	r := chi.NewRouter()

	// Global middleware.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(SecurityHeadersMiddleware)
	r.Use(NewCORSMiddleware(allowedOrigins))
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

	// Audit service — shared across all route groups that need to log actions.
	auditSvc := audit.NewService(pool)
	if vaultSvc != nil && vaultSvc.Configured() {
		tenantSvc.SetSecretStore(vaultSvc)
	}

	// End-user auth service.
	endUserAuthSvc := enduser.NewAuthService(pool)
	if emailService != nil {
		endUserAuthSvc.SetEmailService(emailService)
	}
	if smsService != nil {
		endUserAuthSvc.SetSMSService(smsService)
	}
	// OAuth client_secrets live in the vault — route sign-in through the
	// tenant service for decryption. Without this, SignInWithOAuth returns
	// a clear error.
	endUserAuthSvc.SetOAuthSecretLookup(tenantSvc.GetOAuthClientSecret)

	// API key middleware (for SDK / end-user routes).
	apiKeyMw := auth.NewAPIKeyMiddleware(pool)

	// End-user JWT middleware (optional — anonymous if no token).
	endUserMw := auth.NewEndUserMiddleware()

	// ── Platform routes ──
	r.Route("/platform", func(r chi.Router) {
		// Unauthenticated: platform auth endpoints.
		// Build rate limiter callback for platform auth (avoids import cycle).
		var platformRateCheck auth.AuthRateLimiter
		if limiter != nil {
			platformRateCheck = func(w http.ResponseWriter, r *http.Request, action, identifier string) bool {
				limits := map[string]struct{ limit int; window time.Duration }{
					"platform_signup":    {ratelimit.SignupLimit, ratelimit.SignupWindow},
					"platform_forgot":    {ratelimit.ForgotPasswordLimit, ratelimit.ForgotPasswordWindow},
					"signin_fail":        {ratelimit.SigninFailLimit, ratelimit.SigninFailWindow},
					"signin_fail_record": {ratelimit.SigninFailLimit, ratelimit.SigninFailWindow},
				}
				cfg, ok := limits[action]
				if !ok {
					cfg = struct{ limit int; window time.Duration }{5, 15 * time.Minute}
				}
				return ratelimit.CheckAuthRate(limiter, w, r.Context(), action, identifier, cfg.limit, cfg.window)
			}
		}
		r.Post("/auth/signup", auth.HandlePlatformSignUp(platformAuthSvc, platformRateCheck))
		r.Post("/auth/signin", auth.HandlePlatformSignIn(platformAuthSvc, platformRateCheck))
		r.Post("/auth/forgot-password", auth.HandlePlatformForgotPassword(platformAuthSvc, platformRateCheck))
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

		// Authenticated: accept project invitation (token-based).
		r.Route("/invitations", func(r chi.Router) {
			if isDev {
				r.Use(devAuthMiddleware)
			} else {
				r.Use(platformAuth.Handler)
			}
			r.Post("/accept", tenant.HandleAcceptInvitation(pool))
		})

		// Authenticated: superadmin-only platform administration.
		// These endpoints manage state that spans every tenant (allowlist,
		// cross-tenant project list). Regular project owners must never
		// reach these.
		r.Route("/admin", func(r chi.Router) {
			if isDev {
				r.Use(devAuthMiddleware)
			} else {
				r.Use(platformAuth.Handler)
			}
			r.Use(superadminMiddleware(pool))
			// Inject audit service + actor identity.
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := audit.WithContext(r.Context(), auditSvc)
					if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
						ctx = audit.WithActor(ctx, claims.Subject, claims.Email)
					}
					next.ServeHTTP(w, r.WithContext(ctx))
				})
			})

			r.Get("/projects", tenant.AdminListAllProjects(pool))
			r.Get("/allowlist", tenant.AdminListAllowlist(pool))
			r.Post("/allowlist", tenant.AdminAddAllowlist(pool))
			r.Delete("/allowlist/{email}", tenant.AdminRemoveAllowlist(pool))
			r.Post("/allowlist/email", tenant.AdminSendAllowlistEmail(pool, emailService))
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
			// Verify the authenticated user is a member of this project.
			r.Use(projectMembershipMiddleware(pool, isDev))
			if logCh != nil {
				r.Use(RequestLoggingMiddleware(logCh))
			}
			// Inject audit service + actor identity into every request context.
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := audit.WithContext(r.Context(), auditSvc)
					if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
						ctx = audit.WithActor(ctx, claims.Subject, claims.Email)
					}
					next.ServeHTTP(w, r.WithContext(ctx))
				})
			})
			r.Get("/logs", HandleLogs(pool))
			r.Get("/schema", query.HandleSchemaIntrospection(pool))
			r.Get("/schema/changes", query.HandleSchemaChanges(pool))
			r.Get("/schema/rls-audit", query.HandleRLSAudit(pool))
			r.Mount("/schema/tables", query.HandleDDL(pool))
			r.Mount("/schema/functions", query.HandleFunctions(pool))
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

			// Vault (encrypted secrets storage) — platform-authenticated.
			if vaultSvc != nil && vaultSvc.Configured() {
				r.Mount("/vault", vault.Routes(vaultSvc, pool))
			}

			// Compliance (DPA report, sub-processor registry).
			complianceSvc := compliance.NewComplianceService(pool)
			r.Get("/compliance/dpa-report", compliance.HandleDPAReport(complianceSvc))
			r.Get("/compliance/sub-processors", compliance.HandleSubProcessors(complianceSvc))

			r.Get("/compliance/audit-log", audit.HandleList(auditSvc))

			// Team members (invite, remove, change role).
			var sendEmailFn func(ctx context.Context, to, subject, html string) error
			if emailService != nil {
				sendEmailFn = emailService.SendRaw
			}
			r.Get("/members", tenant.HandleListMembers(pool))
			r.Post("/members/invite", tenant.HandleInviteMember(pool, sendEmailFn))
			r.Post("/members/resend", tenant.HandleResendInvitation(pool, sendEmailFn))
			r.Delete("/members/{userId}", tenant.HandleRemoveMember(pool))
			r.Patch("/members/{userId}", tenant.HandleChangeRole(pool))

			// Edge Functions (serverless compute management).
			fnSvc := functions.NewService(pool)
			fnTrigSvc := functions.NewTriggerService(pool)
			r.Route("/functions", func(r chi.Router) {
				r.Get("/", functions.HandleList(fnSvc))
				r.Post("/", functions.HandleCreate(fnSvc, limitsSvc))
				r.Get("/{name}", functions.HandleGet(fnSvc))
				r.Put("/{name}", functions.HandleUpdate(fnSvc))
				r.Delete("/{name}", functions.HandleDelete(fnSvc))
				r.Get("/{name}/logs", functions.HandleLogs(fnSvc))
				r.Get("/{name}/triggers", functions.HandleListTriggers(fnSvc, fnTrigSvc))
				r.Post("/{name}/triggers", functions.HandleCreateTrigger(fnSvc, fnTrigSvc))
				r.Delete("/{name}/triggers/{triggerId}", functions.HandleDeleteTrigger(fnTrigSvc))
				r.Get("/{name}/versions", functions.HandleListVersions(fnSvc))
				r.Post("/{name}/rollback", functions.HandleRollback(fnSvc))
				r.Get("/{name}/metrics", functions.HandleMetrics(fnSvc))
			})

			// Console end-user management — platform-authenticated.
			r.Route("/users", func(r chi.Router) {
				r.Use(tenant.PlatformTenantContext(pool))
				r.Mount("/", enduser.PlatformRoutes(pool, limiter))
			})

			// Console storage proxy — platform-authenticated access to project storage.
			if s3Client != nil {
				r.Route("/storage", func(r chi.Router) {
					r.Use(tenant.PlatformStorageContext(pool))
					storageHandler := storage.NewStorageHandler(s3Client, pool)
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

				r.Post("/sql", query.HandlePlatformSQL(queryEngine))
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
		// Inject audit service + actor identity so tenant CRUD handlers can log.
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := audit.WithContext(r.Context(), auditSvc)
				if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims != nil {
					ctx = audit.WithActor(ctx, claims.Subject, claims.Email)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

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
		// OAuth callbacks — no API key needed; project resolved via subdomain.
		// These must be outside the apiKeyMw group because the OAuth provider
		// redirects back without forwarding the apikey query parameter.
		r.Get("/auth/oauth/{provider}/callback", enduser.HandleOAuthCallback(endUserAuthSvc))
		r.Post("/auth/oauth/{provider}/callback", enduser.HandleOAuthCallback(endUserAuthSvc)) // Apple form_post

		// Auth endpoints (only need API key, no end-user JWT).
		r.Route("/auth", func(r chi.Router) {
			r.Use(apiKeyMw.Handler)
			r.Post("/signup", enduser.HandleSignUp(endUserAuthSvc, limiter))
			r.Post("/signin", enduser.HandleSignIn(endUserAuthSvc, limiter))
			r.Post("/refresh", enduser.HandleRefresh(endUserAuthSvc))
			r.Post("/signout", enduser.HandleSignOut(endUserAuthSvc))
			r.Post("/forgot-password", enduser.HandleForgotPassword(endUserAuthSvc, limiter))
			r.Post("/reset-password", enduser.HandleResetPassword(endUserAuthSvc))
			r.Post("/verify-email", enduser.HandleVerifyEmail(endUserAuthSvc))
			r.Post("/resend-verification", enduser.HandleResendVerification(endUserAuthSvc, limiter))
			r.Post("/request-magic-link", enduser.HandleRequestMagicLink(endUserAuthSvc, limiter))
			r.Post("/signin-magic-link", enduser.HandleSignInWithMagicLink(endUserAuthSvc))
			r.Get("/oauth/{provider}", enduser.HandleOAuthRedirect(endUserAuthSvc))
			r.Post("/phone/send-otp", enduser.HandleSendPhoneOTP(endUserAuthSvc, limiter))
			r.Post("/phone/verify", enduser.HandleVerifyPhoneOTP(endUserAuthSvc))

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

			// Tenant-scoped DDL via SDK. Must be mounted BEFORE the /{table}
			// wildcard routes so chi resolves /schema/tables to the DDL
			// handlers, not HandleTableGet. Requires a secret key (eb_sk_*)
			// since DDL is destructive. The handlers inside HandleDDL expect
			// chi URL param "id" to be the project ID — sdkDDLAdapter injects
			// it from the API-key-authenticated ProjectContext.
			r.Route("/schema/tables", func(r chi.Router) {
				r.Use(requireSecretKeyForDDL)
				r.Use(sdkDDLAdapter)
				r.Mount("/", query.HandleDDL(pool))
			})

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

				storageHandler := storage.NewStorageHandler(s3Client, pool)
				r.Mount("/", storageHandler.Routes())
			})
		} else {
			slog.Warn("s3 client not configured, storage routes disabled")
		}

		// Vault routes (API key authenticated, secret key only).
		if vaultSvc != nil && vaultSvc.Configured() {
			r.Route("/vault", func(r chi.Router) {
				r.Use(apiKeyMw.Handler)
				r.Get("/", vault.HandleSDKList(vaultSvc))
				r.Get("/{name}", vault.HandleSDKGet(vaultSvc))
				r.Post("/", vault.HandleSDKSet(vaultSvc, pool))
				r.Delete("/{name}", vault.HandleSDKDelete(vaultSvc))
			})
		}

		// Edge Functions invocation (API key + optional end-user JWT).
		sdkFnSvc := functions.NewService(pool)
		r.Route("/functions", func(r chi.Router) {
			r.Use(apiKeyMw.Handler)
			r.Use(endUserMw.Handler)
			r.HandleFunc("/{name}", functions.HandleInvoke(pool, sdkFnSvc, fnRunnerURL))
		})
	})

	return r
}

// projectMembershipMiddleware verifies the authenticated user is a member of
// the project identified by the {id} URL parameter. Returns 404 if not.
func projectMembershipMiddleware(pool *pgxpool.Pool, isDev bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// In dev mode, skip membership check (dev user may not have real membership).
			if isDev {
				next.ServeHTTP(w, r)
				return
			}

			claims, ok := auth.ClaimsFromContext(r.Context())
			if !ok || claims == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			projectID := chi.URLParam(r, "id")
			if projectID == "" {
				http.Error(w, `{"error":"missing project id"}`, http.StatusBadRequest)
				return
			}

			role, err := tenant.ResolveRole(r.Context(), pool, projectID, claims.Subject)
			if err != nil || role == "" {
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// superadminMiddleware gates routes to platform superadmins only. The flag
// is read from the Claims set by platformAuth.Handler (which in turn gets
// it from the JWT issued at sign-in). For sensitive actions, the handler
// itself should re-verify from platform_users in case the flag was revoked
// after the token was issued.
func superadminMiddleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.ClaimsFromContext(r.Context())
			if !ok || claims == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if !claims.IsSuperadmin {
				http.Error(w, `{"error":"superadmin only"}`, http.StatusForbidden)
				return
			}
			// Re-verify against the DB. A stolen token or stale flag shouldn't
			// grant platform-wide access — the per-request DB hit is cheap
			// compared to the blast radius.
			var stillSuper bool
			if err := pool.QueryRow(r.Context(),
				`SELECT COALESCE(is_superadmin, false) FROM platform_users WHERE id = $1`,
				claims.Subject,
			).Scan(&stillSuper); err != nil || !stillSuper {
				http.Error(w, `{"error":"superadmin only"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireSecretKeyForDDL gates SDK DDL routes to secret API keys only.
// Public keys (eb_pk_*) live in client-side code and must not be able to
// run destructive schema operations.
func requireSecretKeyForDDL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}
		if pc.KeyType != "secret" {
			http.Error(w, `{"error":"schema DDL requires a secret API key (eb_sk_*)"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sdkDDLAdapter injects the authenticated ProjectContext.ProjectID as the
// chi URL param "id" that HandleDDL's handlers expect. This lets the same
// handlers serve both the platform path (/platform/projects/{id}/schema/...)
// where {id} comes from the URL and the SDK path (/v1/db/schema/...) where
// the project is resolved by the API key middleware.
func sdkDDLAdapter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok || pc.ProjectID == "" {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			rctx.URLParams.Add("id", pc.ProjectID)
		}
		next.ServeHTTP(w, r)
	})
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
