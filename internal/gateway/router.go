package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/enduser"
	"github.com/eurobase/euroback/internal/functions"
	"github.com/eurobase/euroback/internal/metrics"
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
// `pool` is the gateway runtime pool, wired to eurobase_gateway. Used for
// SDK runtime traffic and queries against public.* metadata.
//
// `developerPool` is the platform-authenticated pool, wired to
// eurobase_developer (member of eurobase_migrator). Used only for routes
// that run developer-authored SQL/DDL on tenant schemas. Pass nil to
// fall back to the gateway pool for those routes (acceptable in local
// dev before the eurobase_developer role is bootstrapped; production
// callers should always pass a real pool).
//
// When devMode is true, the platform auth middleware is replaced with a
// pass-through that injects a fixed test user (for local curl/Postman testing).
// devMode must NEVER be enabled in production.
func NewRouter(pool *pgxpool.Pool, developerPool *pgxpool.Pool, platformAuth *auth.PlatformAuthMiddleware, platformAuthSvc *auth.PlatformAuthService, limiter *ratelimit.RateLimiter, s3Client *storage.S3Client, hub *realtime.Hub, logCh chan<- LogEntry, subdomainMw *auth.SubdomainMiddleware, emailService *email.EmailService, smsService *sms.Service, limitsSvc *plans.LimitsService, vaultSvc *vault.VaultService, fnRunnerURL string, fnSigner *functions.Signer, fnRunnerHMACSecret string, metricsReg *metrics.Registry, allowedOrigins []string, devMode ...bool) chi.Router {
	// Local dev fallback: if no developer pool is provided, reuse the
	// gateway pool. The engine will still try `SET LOCAL ROLE
	// eurobase_migrator` and fail with a clear error, which is the
	// signal to bootstrap the developer role.
	if developerPool == nil {
		developerPool = pool
	}
	r := chi.NewRouter()

	// Global middleware.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(SecurityHeadersMiddleware)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Prometheus request metrics — must run after chi has matched the route
	// so RoutePattern() is populated. chi runs middleware in registration
	// order but records the pattern before invoking the final handler, so
	// wrapping here captures everything.
	if metricsReg != nil {
		r.Use(metricsReg.Middleware)
	}

	// Subdomain resolution — resolves {slug}.eurobase.app to a project context.
	// Must run BEFORE CORS so per-project cors_origins can be looked up
	// during the preflight (browsers strip auth headers on OPTIONS, so
	// the apikey middleware can't be the source of project context for
	// preflight).
	if subdomainMw != nil {
		r.Use(subdomainMw.Handler)
	}

	// CORS — checks origin against the global allowlist, then against
	// the per-project cors_origins from AuthConfig if a subdomain
	// resolved a project. See cors.go for the full layering.
	r.Use(NewCORSMiddleware(allowedOrigins))

	isDev := len(devMode) > 0 && devMode[0]

	// Health check (unauthenticated).
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Internal storage RPC for the functions runner. Closes #85.
	// Routes are HMAC-authenticated (no apikey, no JWT). The Ingress
	// only exposes /v1, /platform, /health — so this path is unreachable
	// from outside the cluster. Only the functions runner pod has the
	// HMAC secret. Mounted only when both the secret and an S3 client
	// are available.
	if fnRunnerHMACSecret != "" && s3Client != nil {
		ish, err := functions.NewInternalStorageHandler(pool, s3Client, fnRunnerHMACSecret)
		if err != nil {
			slog.Warn("internal storage handler not mounted", "error", err)
		} else {
			r.Mount("/internal/functions/storage", ish.Routes())
			slog.Info("internal storage RPC enabled at /internal/functions/storage")
		}
	}

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

	// PAT service — shared with platformAuth (via WithPATService in main.go)
	// for token validation, and used directly here by the CRUD handlers.
	patSvc := auth.NewPATService(pool)

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

			// Personal Access Tokens.
			r.Get("/tokens", auth.HandleListPATs(patSvc))
			r.Post("/tokens", auth.HandleCreatePAT(patSvc))
			r.Delete("/tokens/{id}", auth.HandleRevokePAT(patSvc))
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
			// Per-route role gates — closes #50. The mapping mirrors
			// the user-facing Role Permissions table (Members tab):
			//   View data/logs/compliance → viewer
			//   Edit data/schema/functions → developer
			//   Settings/API keys/vault/invites → admin
			//   Delete project / change roles → owner
			r.With(tenant.RequireMinRole("viewer")).Get("/logs", HandleLogs(pool))
			r.With(tenant.RequireMinRole("viewer")).Get("/schema", query.HandleSchemaIntrospection(pool))
			r.With(tenant.RequireMinRole("viewer")).Get("/schema/changes", query.HandleSchemaChanges(pool))
			r.With(tenant.RequireMinRole("viewer")).Get("/schema/rls-audit", query.HandleRLSAudit(pool))
			// DDL on tenant schemas runs against the developer pool so
			// CREATE/ALTER/REFERENCES on migrator-owned tables works.
			// withDeveloperRole adds the flag the DDL helpers read inside
			// runDDL to elevate `SET LOCAL ROLE eurobase_migrator` — without
			// it, tables would be developer-owned and cross-tool DDL would
			// fail with "must be owner". (PlatformTenantContext also sets
			// this flag, but it's only mounted on /data/sql and below.)
			r.With(tenant.RequireMinRole("developer"), withDeveloperRole).Mount("/schema/tables", query.HandleDDL(developerPool))
			r.With(tenant.RequireMinRole("developer"), withDeveloperRole).Mount("/schema/functions", query.HandleFunctions(developerPool))
			// Tenant-level versioned migrations (#190) — same trust plane
			// as the schema DDL endpoints above: platform auth, developer
			// role, migrator elevation on the developer pool. Deliberately
			// NOT exposed on /v1: data-plane keys never run DDL.
			r.With(tenant.RequireMinRole("developer"), withDeveloperRole).Mount("/migrations", query.HandleTenantMigrations(developerPool, pool))
			r.With(tenant.RequireMinRole("developer")).Mount("/webhooks", webhook.Routes(pool, limitsSvc))
			cronSvc := cron.NewCronService(pool)
			r.With(tenant.RequireMinRole("developer")).Mount("/cron", cron.Routes(cronSvc))
			r.With(tenant.RequireMinRole("viewer")).Get("/api-keys", tenant.HandleListAPIKeys(pool))
			r.With(tenant.RequireMinRole("admin")).Post("/api-keys/regenerate", tenant.HandleRegenerateAPIKeys(pool))
			r.With(tenant.RequireMinRole("viewer")).Get("/connect", tenant.HandleConnect(pool))

			// Plan usage.
			if limitsSvc != nil {
				r.With(tenant.RequireMinRole("viewer")).Get("/usage", plans.HandleGetUsage(limitsSvc, pool))
			}

			// Email template management.
			if emailService != nil {
				tmplHandler := email.NewTemplateHandler(pool, emailService, limitsSvc)
				r.With(tenant.RequireMinRole("viewer")).Get("/email-templates", tmplHandler.HandleList())
				r.With(tenant.RequireMinRole("developer")).Put("/email-templates/{type}", tmplHandler.HandleUpdate())
				r.With(tenant.RequireMinRole("developer")).Delete("/email-templates/{type}", tmplHandler.HandleDelete())
				r.With(tenant.RequireMinRole("developer")).Post("/email-templates/{type}/preview", tmplHandler.HandlePreview())
				r.With(tenant.RequireMinRole("developer")).Post("/email-templates/{type}/test", tmplHandler.HandleTest())
			}

			// Vault (encrypted secrets storage) — platform-authenticated.
			if vaultSvc != nil && vaultSvc.Configured() {
				r.With(tenant.RequireMinRole("admin")).Mount("/vault", vault.Routes(vaultSvc, pool))
			}

			// Compliance (DPA report, sub-processor registry, DSAR exports).
			complianceSvc := compliance.NewComplianceService(pool)
			r.With(tenant.RequireMinRole("viewer")).Get("/compliance/dpa-report", compliance.HandleDPAReport(complianceSvc))
			r.With(tenant.RequireMinRole("viewer")).Get("/compliance/sub-processors", compliance.HandleSubProcessors(complianceSvc))

			r.With(tenant.RequireMinRole("viewer")).Get("/compliance/audit-log", audit.HandleList(auditSvc))
			r.With(tenant.RequireMinRole("admin")).Get("/compliance/audit-log/verify", audit.HandleVerify(auditSvc))

			// DSAR exports (tenant-level and per-user). Triggering an
			// export pulls every row from every tenant table, so this
			// is a "settings"-shaped capability — minimum admin per #50.
			// Listing + status are also admin-only since the URLs they
			// hand back are presigned and give the holder the file.
			exportSvc := compliance.NewExportService(pool, s3Client, auditSvc)
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/export", compliance.HandleRequestTenantExport(exportSvc))
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/user-export", compliance.HandleRequestUserExport(exportSvc))
			r.With(tenant.RequireMinRole("admin")).Get("/compliance/exports", compliance.HandleListExports(exportSvc))
			r.With(tenant.RequireMinRole("admin")).Get("/compliance/exports/{exportId}", compliance.HandleGetExport(exportSvc))

			// Team members (invite, remove, change role).
			var sendEmailFn func(ctx context.Context, to, subject, html string) error
			if emailService != nil {
				sendEmailFn = emailService.SendRaw
			}
			// Member CRUD: handler-level RequireRole already enforces
			// these levels via a second DB lookup; the middleware gate
			// short-circuits before that DB call. Belt-and-braces; the
			// inner check stays so the cleanup is a separate follow-up.
			r.With(tenant.RequireMinRole("viewer")).Get("/members", tenant.HandleListMembers(pool))
			r.With(tenant.RequireMinRole("admin")).Post("/members/invite", tenant.HandleInviteMember(pool, sendEmailFn))
			r.With(tenant.RequireMinRole("admin")).Post("/members/resend", tenant.HandleResendInvitation(pool, sendEmailFn))
			r.With(tenant.RequireMinRole("admin")).Delete("/members/{userId}", tenant.HandleRemoveMember(pool))
			r.With(tenant.RequireMinRole("owner")).Patch("/members/{userId}", tenant.HandleChangeRole(pool))

			// Edge Functions (serverless compute management).
			fnSvc := functions.NewService(pool)
			fnTrigSvc := functions.NewTriggerService(pool)
			r.Route("/functions", func(r chi.Router) {
				// Reads → viewer; mutations → developer (closes #50).
				r.With(tenant.RequireMinRole("viewer")).Get("/", functions.HandleList(fnSvc))
				r.With(tenant.RequireMinRole("developer")).Post("/", functions.HandleCreate(fnSvc, limitsSvc))
				r.With(tenant.RequireMinRole("viewer")).Get("/{name}", functions.HandleGet(fnSvc))
				r.With(tenant.RequireMinRole("developer")).Put("/{name}", functions.HandleUpdate(fnSvc))
				r.With(tenant.RequireMinRole("developer")).Delete("/{name}", functions.HandleDelete(fnSvc))
				r.With(tenant.RequireMinRole("viewer")).Get("/{name}/logs", functions.HandleLogs(fnSvc))
				r.With(tenant.RequireMinRole("viewer")).Get("/{name}/triggers", functions.HandleListTriggers(fnSvc, fnTrigSvc))
				r.With(tenant.RequireMinRole("developer")).Post("/{name}/triggers", functions.HandleCreateTrigger(fnSvc, fnTrigSvc))
				r.With(tenant.RequireMinRole("developer")).Delete("/{name}/triggers/{triggerId}", functions.HandleDeleteTrigger(fnTrigSvc))
				r.With(tenant.RequireMinRole("viewer")).Get("/{name}/versions", functions.HandleListVersions(fnSvc))
				r.With(tenant.RequireMinRole("developer")).Post("/{name}/rollback", functions.HandleRollback(fnSvc))
				r.With(tenant.RequireMinRole("viewer")).Get("/{name}/metrics", functions.HandleMetrics(fnSvc))
			})

			// Console end-user management — platform-authenticated.
			// End-user admin is a "settings"-shaped capability (closes #50).
			r.Route("/users", func(r chi.Router) {
				r.Use(tenant.RequireMinRole("admin"))
				r.Use(tenant.PlatformTenantContext(pool))
				r.Mount("/", enduser.PlatformRoutes(pool, limiter))
			})

			// Console storage proxy — platform-authenticated access to project storage.
			// Reads → viewer; uploads/deletes → developer (closes #50).
			// We bypass storageHandler.Routes() here so each method gets
			// its own gate; the SDK mount keeps using Routes() unchanged.
			if s3Client != nil {
				r.Route("/storage", func(r chi.Router) {
					r.Use(tenant.PlatformStorageContext(pool))
					storageHandler := storage.NewStorageHandler(s3Client, pool, query.NewQueryEngine(pool))
					r.With(tenant.RequireMinRole("developer")).Post("/upload", storageHandler.UploadFile)
					r.With(tenant.RequireMinRole("developer")).Post("/signed-url", storageHandler.GenerateSignedURL)
					r.With(tenant.RequireMinRole("viewer")).Get("/", storageHandler.ListFiles)
					r.With(tenant.RequireMinRole("viewer")).Get("/*", storageHandler.DownloadFile)
					r.With(tenant.RequireMinRole("developer")).Delete("/*", storageHandler.DeleteFile)
				})
			}

			// Console data proxy — platform-authenticated access to project data.
			// Note: {id} here shadows the outer {id} (project ID) which is fine —
			// PlatformTenantContext already resolved the project in middleware.
			//
			// Uses the developer pool: PlatformTenantContext sets the
			// developer-role flag, and the engine elevates each tx to
			// eurobase_migrator so DDL/REFERENCES against migrator-owned
			// tables work for the authenticated developer. The membership
			// check in the middleware (ResolveRole, line 64 of
			// internal/tenant/context.go) still uses the gateway pool —
			// that's a public.* read.
			r.Route("/data", func(r chi.Router) {
				r.Use(tenant.PlatformTenantContext(pool))

				queryEngine := query.NewQueryEngine(developerPool)
				publisher := realtime.NewEventPublisher(nil, hub)

				// Reads → viewer; mutations + SQL exec → developer
				// (closes #50). HandlePlatformSQL{,Transaction} can run
				// arbitrary SQL including DDL, so they belong in the
				// "Edit data, schema, functions" row.
				r.With(tenant.RequireMinRole("developer")).Post("/sql", query.HandlePlatformSQL(queryEngine))
				r.With(tenant.RequireMinRole("developer")).Post("/sql/transaction", query.HandlePlatformSQLTransaction(queryEngine))
				r.With(tenant.RequireMinRole("viewer")).Get("/{table}", query.HandleTableGet(queryEngine))
				r.With(tenant.RequireMinRole("viewer")).Get("/{table}/{id}", query.HandleTableGetByID(queryEngine))
				r.With(tenant.RequireMinRole("developer")).Post("/{table}", query.HandleTableInsert(queryEngine, publisher))
				r.With(tenant.RequireMinRole("developer")).Post("/{table}/bulk-delete", query.HandleTableBulkDelete(queryEngine, publisher))
				r.With(tenant.RequireMinRole("developer")).Patch("/{table}/{id}", query.HandleTableUpdate(queryEngine, publisher))
				r.With(tenant.RequireMinRole("developer")).Delete("/{table}/{id}", query.HandleTableDelete(queryEngine, publisher))
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
	// Closes #62. Authorize takes (token, project_id) and supports both
	// platform JWTs (verified via platformAuth + project_members
	// membership check) and end-user JWTs (verified against the
	// project's own jwt_secret). Returns the project's plan so the hub
	// can enforce per-project connection limits.
	if hub != nil {
		var authorize realtime.Authorize
		if !isDev {
			authorize = buildRealtimeAuthorize(pool, platformAuth)
		}
		wsHandler := realtime.HandleWebSocket(hub, authorize, BuildOriginChecker(allowedOrigins), isDev)
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
				// DSAR self-serve: end-user exports their own data.
				r.Post("/me/export", compliance.HandleSelfServeExport(pool, s3Client, auditSvc))
				r.Get("/me/export/{exportId}", compliance.HandleSelfServeExportStatus(pool, s3Client, auditSvc))
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
			//
			// Routed through developerPool (not the runtime gateway pool) +
			// the developer-role flag so all SDK DDL elevates to
			// eurobase_migrator inside its tx, producing migrator-owned
			// tables identical to the platform/MCP DDL path. Without this,
			// SDK-created tables are gateway-owned and cannot be ALTER/DROP'd
			// from the platform path (and vice versa). See issues #40/#41/#42.
			r.Route("/schema/tables", func(r chi.Router) {
				r.Use(requireSecretKeyForDDL)
				r.Use(sdkDDLAdapter)
				r.Mount("/", query.HandleDDL(developerPool))
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

				storageHandler := storage.NewStorageHandler(s3Client, pool, query.NewQueryEngine(pool))
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
			r.HandleFunc("/{name}", functions.HandleInvoke(pool, sdkFnSvc, fnRunnerURL, fnSigner))
		})

		// Schedules — SDK control-plane for cron jobs. Closes #112.
		// Service-key only: editing schedules is destructive (writes to
		// cron_jobs) and public keys live in client code.
		r.Route("/schedules", func(r chi.Router) {
			r.Use(apiKeyMw.Handler)
			r.Use(requireSecretKeyForSchedules)
			sdkCronSvc := cron.NewCronService(pool)
			r.Mount("/", cron.SDKRoutes(sdkCronSvc))
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

			// Stash the resolved role on the context so per-route
			// middleware (tenant.RequireMinRole) can gate without a
			// second DB hit. Closes #50.
			next.ServeHTTP(w, r.WithContext(tenant.WithRole(r.Context(), role)))
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

// withDeveloperRole flags the request context for eurobase_migrator role
// elevation inside DDL transactions (see internal/query/engine.go
// applyDeveloperRole). Apply to platform-authenticated DDL routes that
// don't already go through tenant.PlatformTenantContext (which sets the
// same flag).
func withDeveloperRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(query.WithDeveloperRole(r.Context())))
	})
}

// requireSecretKeyForSchedules gates SDK `/v1/schedules` to secret API
// keys only. Schedules are control-plane state — they fire arbitrary
// edge-function invocations on a recurring cadence. Public keys live in
// client-side code and a leaked public key must not be able to install
// or remove a schedule.
func requireSecretKeyForSchedules(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}
		if pc.KeyType != "secret" {
			http.Error(w, `{"error":"managing schedules requires a secret API key (eb_sk_*)"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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
// chi URL param "id" that HandleDDL's handlers expect, and flags the
// request context for eurobase_migrator role elevation inside the DDL
// transactions (see internal/query/engine.go applyDeveloperRole).
//
// This lets the same handlers serve both the platform path
// (/platform/projects/{id}/schema/...) where {id} comes from the URL and
// the developer-role flag is set by tenant.PlatformTenantContext, and the
// SDK path (/v1/db/schema/...) where the project is resolved by the API
// key middleware and the dev-role flag is set here. Both paths therefore
// produce uniformly migrator-owned tables.
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
		ctx := query.WithDeveloperRole(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// buildRealtimeAuthorize returns a realtime.Authorize closure that
// validates the WebSocket token. Closes #62 (project routing) and
// extends per #108 to attach the per-row filter identity.
//
// Three token shapes are accepted:
//
//  1. **API key** (`eb_pk_…` / `eb_sk_…`) — resolved server-side to a
//     project. `?project_id=` may be omitted; if provided it must
//     match the apikey's project. `eb_sk_*` is treated as service-role
//     (sees every row); `eb_pk_*` is anonymous (sees only rows
//     without an owner column).
//  2. **Platform JWT** — validated against the platform secret,
//     subject is a platform user. Requires `?project_id=` and a
//     project_members row. Treated as service-role.
//  3. **End-user JWT** — validated against the project's own
//     jwt_secret. Requires `?project_id=` matching the JWT's claim.
//     The JWT's subject is threaded down as the realtime filter
//     identity so the hub only delivers rows where the owner column
//     matches.
//
// Returns ErrUnauthorized for a bad token, ErrForbidden for a valid
// token without access.
func buildRealtimeAuthorize(pool *pgxpool.Pool, platformAuth *auth.PlatformAuthMiddleware) realtime.Authorize {
	return func(ctx context.Context, token, requestedProjectID string) (realtime.AuthorizedClient, error) {
		// 1. API key path — covers the SDK realtime use case.
		if strings.HasPrefix(token, "eb_pk_") || strings.HasPrefix(token, "eb_sk_") {
			pc, err := auth.ResolveAPIKey(ctx, pool, token)
			if err != nil {
				return realtime.AuthorizedClient{}, realtime.ErrUnauthorized
			}
			if requestedProjectID != "" && requestedProjectID != pc.ProjectID {
				return realtime.AuthorizedClient{}, realtime.ErrForbidden
			}
			return realtime.AuthorizedClient{
				ProjectID: pc.ProjectID,
				Plan:      pc.Plan,
				Service:   pc.KeyType == "secret",
			}, nil
		}

		// Beyond the apikey path a project_id is required: the JWT
		// alone doesn't unambiguously pick a project (platform users
		// can be in many projects; end-user JWTs name one explicitly
		// and we cross-check against the query param).
		if requestedProjectID == "" {
			return realtime.AuthorizedClient{}, realtime.ErrUnauthorized
		}

		// 2. Platform JWT path — subject is platform_users.id; require
		//    membership on the requested project. Platform users
		//    are admins for the project, so service=true (they see
		//    every row regardless of owner column).
		if subject, err := platformAuth.ValidateToken(token); err == nil && subject != "" {
			role, roleErr := tenant.ResolveRole(ctx, pool, requestedProjectID, subject)
			if roleErr != nil {
				return realtime.AuthorizedClient{}, fmt.Errorf("resolve role: %w", roleErr)
			}
			if role == "" {
				return realtime.AuthorizedClient{}, realtime.ErrForbidden
			}
			var plan string
			if err := pool.QueryRow(ctx,
				`SELECT COALESCE(plan, 'free') FROM projects WHERE id = $1 AND status = 'active'`,
				requestedProjectID,
			).Scan(&plan); err != nil {
				return realtime.AuthorizedClient{}, fmt.Errorf("load project plan: %w", err)
			}
			return realtime.AuthorizedClient{
				ProjectID: requestedProjectID,
				Plan:      plan,
				Service:   true,
			}, nil
		}

		// 3. End-user JWT — validated against the requested project's
		//    own jwt_secret. The JWT's project_id claim must match.
		var jwtSecret, plan string
		err := pool.QueryRow(ctx,
			`SELECT jwt_secret, COALESCE(plan, 'free') FROM projects WHERE id = $1 AND status = 'active'`,
			requestedProjectID,
		).Scan(&jwtSecret, &plan)
		if err != nil {
			return realtime.AuthorizedClient{}, realtime.ErrUnauthorized
		}
		claims, err := auth.ValidateEndUserJWT(token, jwtSecret)
		if err != nil || claims == nil {
			return realtime.AuthorizedClient{}, realtime.ErrUnauthorized
		}
		if claims.ProjectID != "" && claims.ProjectID != requestedProjectID {
			return realtime.AuthorizedClient{}, realtime.ErrForbidden
		}
		return realtime.AuthorizedClient{
			ProjectID: requestedProjectID,
			Plan:      plan,
			EndUserID: claims.UserID,
		}, nil
	}
}

// devAuthMiddleware injects a test user for local development.
// An Authorization header must still be present (any value works), so that
// "no auth" requests are correctly rejected with 401.
// This middleware is wired only when DEV_MODE=true, which is fenced at
// startup against production hosts (cmd/gateway/main.go).
//
// Closes #60: subject/email come from env vars (DEV_AUTH_SUBJECT /
// DEV_AUTH_EMAIL) so the binary itself doesn't carry a hardcoded
// backdoor identity. Defaults are kept for ergonomic local dev.
const (
	defaultDevSubject = "00000000-0000-0000-0000-000000000001"
	defaultDevEmail   = "dev@eurobase.eu"
)

func devAuthMiddleware(next http.Handler) http.Handler {
	subject := os.Getenv("DEV_AUTH_SUBJECT")
	if subject == "" {
		subject = defaultDevSubject
	}
	email := os.Getenv("DEV_AUTH_EMAIL")
	if email == "" {
		email = defaultDevEmail
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}
		ctx := auth.ContextWithClaims(r.Context(), &auth.Claims{
			Subject: subject,
			Email:   email,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
