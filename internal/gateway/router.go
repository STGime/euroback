package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/audit/export"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/billing"
	"github.com/eurobase/euroback/internal/breach"
	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/enduser"
	"github.com/eurobase/euroback/internal/functions"
	"github.com/eurobase/euroback/internal/metrics"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/eurobase/euroback/internal/sms"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/eurobase/euroback/internal/vault"
	"github.com/eurobase/euroback/internal/webhook"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
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
func NewRouter(pool *pgxpool.Pool, developerPool *pgxpool.Pool, migrationExec *query.MigrationExecutor, platformAuth *auth.PlatformAuthMiddleware, platformAuthSvc *auth.PlatformAuthService, limiter *ratelimit.RateLimiter, accessRecorder *audit.AccessRecorder, s3Client *storage.S3Client, hub *realtime.Hub, logCh chan<- LogEntry, subdomainMw *auth.SubdomainMiddleware, emailService *email.EmailService, smsService *sms.Service, limitsSvc *plans.LimitsService, vaultSvc *vault.VaultService, fnRunnerURL string, fnSigner *functions.Signer, fnRunnerHMACSecret string, metricsReg *metrics.Registry, allowedOrigins []string, unsubSigner *email.UnsubscribeSigner, billingSvc *billing.Service, devMode ...bool) chi.Router {
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
	// Team-tier closed-beta: wire the billing service as the beta-grant
	// recorder so tenant.CreateProject can drop a `beta_grant`
	// subscription row when a granted user creates their first Team
	// project. Skipped if billing is nil (dev environments without
	// Mollie config); the project still provisions.
	if billingSvc != nil {
		tenantSvc.SetBetaGrantRecorder(billingSvc)
	}

	// Team-tier M3 (backup + PITR). Same provider registry shape as
	// cmd/worker/main.go — Scaleway wired against SCW_SECRET_KEY /
	// SCW_PROJECT_ID. Empty secret is legal (dev environments); the
	// provider's methods return ErrUnauthorized without hitting the
	// network, and the backup handlers surface that as 502.
	scwRegion := os.Getenv("SCW_RDB_REGION")
	if scwRegion == "" {
		scwRegion = "fr-par"
	}
	providerRegistry := dbprovider.NewRegistry()
	providerRegistry.Register(dbprovider.NewScaleway(dbprovider.ScalewayConfig{
		SecretKey:     os.Getenv("SCW_SECRET_KEY"),
		ProjectID:     os.Getenv("SCW_PROJECT_ID"),
		DefaultRegion: scwRegion,
	}))
	// Wire the registry into TenantService so DeleteProject can
	// synchronously best-effort tear down any dedicated instances
	// before hard-deleting the project_databases rows. See
	// TenantService.DeleteProject for why we need this — the FK
	// on projects.id is ON DELETE RESTRICT and doesn't respect
	// deleted_at, so soft-deleted rows from prior failed provisioning
	// alone will block the delete.
	tenantSvc.SetProviderRegistry(providerRegistry)

	// Cipher for the direct-DATABASE_URL surface (M4). Reuses the
	// vault master key already required by the vault package.
	// Nil in dev environments without the secret — the connection
	// handlers are omitted from the router in that case (rather
	// than mounted-but-broken).
	var connCipher *dbprovider.Cipher
	if vk := os.Getenv("VAULT_ENCRYPTION_KEY"); vk != "" {
		if c, err := dbprovider.NewCipher(vk, 1); err != nil {
			slog.Warn("dbprovider cipher init failed — /connection routes disabled", "error", err)
		} else {
			connCipher = c
		}
	} else {
		slog.Warn("VAULT_ENCRYPTION_KEY not set — /connection routes disabled")
	}

	// Per-project pool cache — Team-tier M2.5. Routes SDK tenant
	// traffic to the project's dedicated managed-PG instance when
	// HasDedicatedDB=true. Free/Pro traffic transparently keeps the
	// shared pool. Requires the cipher (uses it to open sealed
	// passwords); without it we ship no routing (fail closed = fall
	// back to shared, which is the pre-M2.5 behaviour).
	//
	// Feature flag TEAM_TIER_ROUTING=1 gates the cache. Ships OFF in
	// prod by default because the *follow-up* work — provisioning the
	// tenant schema + tenant helpers on the dedicated instance at
	// project-create time — is a separate PR. Without that, a
	// dedicated instance is empty; routing SDK queries to it would
	// hit "schema not found" instead of falling back gracefully.
	//
	// ⚠️  DO NOT FLIP THIS ON UNTIL PART 2 SHIPS A NON-OWNER RUNTIME
	// ROLE ON THE DEDICATED INSTANCE. The pool cache currently
	// opens its DSN with rec.Username which is the *owner* role. In
	// Postgres, RLS policies are SKIPPED for a table's owner unless
	// the table has FORCE ROW LEVEL SECURITY. If SDK traffic reaches
	// tenant tables as the owner, `applyRLSContext`'s app.end_user_*
	// GUCs are set but never consulted, and every end-user of a
	// Team-tier app could read/write every other end-user's rows.
	// Part 2 MUST provision a non-owner tenant runtime role
	// (mirroring `eurobase_gateway`'s non-owner status on the shared
	// cluster) and point project_databases.username at it before
	// this flag can be safely flipped. A regression test asserting
	// RLS isolation on the dedicated instance is the safety gate.
	// Create the pool cache whenever the connection cipher is available.
	// The cache is passive — it opens a dedicated pool only when a caller
	// asks for one — so its mere existence does not change SDK-runtime
	// behavior. The SDK-runtime routing (poolResolver below) still gates
	// on TEAM_TIER_ROUTING=1 because that path has the RLS-owner concern
	// documented in the big warning above. Other callers (usage-tracker,
	// #372 follow-up) route unconditionally because they issue platform-
	// side reads (count/sum) that have no RLS-bypass footgun.
	var poolCache *dbprovider.PoolCache
	if connCipher != nil {
		repo := dbprovider.NewRepo(pool)
		poolCache = dbprovider.NewPoolCache(connCipher, repo, 30*time.Minute, 8)
	}
	enableSDKRouting := poolCache != nil && os.Getenv("TEAM_TIER_ROUTING") == "1"
	if enableSDKRouting {
		slog.Info("Team-tier SDK routing enabled (TEAM_TIER_ROUTING=1)")
	}

	// Wire the pool cache into LimitsService so tenant-schema queries
	// (db size / storage / MAU) route to the dedicated instance for
	// Team-tier projects. Without this, usage-tracker queries hit the
	// shared platform DB and log SQLSTATE 42P01 ("relation … does not
	// exist") on every poll for every Team-tier project. Returning nil
	// on ErrNoRows lets Free/Pro (no project_databases row) transparently
	// fall through to the shared pool.
	if poolCache != nil && limitsSvc != nil {
		limitsSvc.WithPoolResolver(func(ctx context.Context, projectID string) *pgxpool.Pool {
			// GetOwner (not Get): the usage queries in
			// internal/plans/usage.go run bare tenant-schema aggregates
			// (COUNT / SUM on users, storage_objects) with NO
			// RunAsService wrapper — so no app.end_user_role='service'
			// GUC is set. The dedicated RLS policies read
			//   USING (is_service_role() OR id = current_end_user_id())
			// so without the service marker, a non-owner runtime pool
			// returns ZERO ROWS → mau_count / storage_size_mb silently
			// report 0. #377's original Get call was harmless while the
			// runtime slot was NULL (EffectiveCredential fell back to
			// owner and bypassed RLS by ownership) — PR-B's backfill
			// changes that: real runtime creds → real RLS enforcement
			// → silent zero. Owner pool avoids the trap because the
			// owner bypasses RLS for owned tables (which every tenant-
			// schema table is).
			p, err := poolCache.GetOwner(ctx, projectID)
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					slog.Warn("usage: dedicated pool unavailable, falling back to shared",
						"project_id", projectID, "error", err)
				}
				return nil
			}
			return p
		})
	}

	// One closure over poolCache.GetOwner used by every console
	// resolver — enduser, storage, PlatformTenantContext (for DDL
	// via TenantPoolFromContext), etc. Same body, three named types
	// (enduser.PoolResolver / tenant.TenantPoolResolver / etc.) —
	// Go's named-type conversion rules mean we can't `cast` a plain
	// closure into each, so we build one closure and let each named
	// type wrap it below.
	//
	// Returns the OWNER pool for consistency with the query engine
	// path: the router-level poolResolver above hands owner pools to
	// console traffic (DeveloperRoleFromContext), so a single
	// console request that touches both the engine (e.g.
	// assertObjectVisible for storage) and a direct h.pool path
	// (e.g. storage_objects INSERT) uses the same role end-to-end.
	// Mixing owner + runtime within one request would be a subtle
	// inconsistency someone would trip over later.
	//
	// Safety: these handlers already run RunAsService / RunAsAuthService
	// which set app.end_user_role='service', so RLS is bypassed by
	// design for platform admin traffic — the owner vs runtime
	// choice doesn't change reachable rows, only who owns any
	// newly-created objects (relevant for future GRANT/RLS
	// invariants on Team-tier).
	//
	// Same nil-on-error contract as the LimitsService resolver above.
	var ownerPoolFor func(ctx context.Context, projectID string) *pgxpool.Pool
	if poolCache != nil {
		ownerPoolFor = func(ctx context.Context, projectID string) *pgxpool.Pool {
			p, err := poolCache.GetOwner(ctx, projectID)
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					slog.Warn("console: dedicated pool unavailable, falling back to shared",
						"project_id", projectID, "error", err)
				}
				return nil
			}
			return p
		}
	}
	var enduserPoolResolver enduser.PoolResolver
	var tenantPoolResolver tenant.TenantPoolResolver
	if ownerPoolFor != nil {
		enduserPoolResolver = ownerPoolFor
		tenantPoolResolver = ownerPoolFor
	}

	// poolResolver bridges query.PoolResolver → poolCache without
	// dragging auth into the query package (cycle). Consulted at
	// every WithTenantTx call. Returning nil = "use shared pool";
	// the query engine handles the fall-through.
	//
	// Two intents share this resolver:
	//
	//   * SDK traffic — gated on TEAM_TIER_ROUTING=1 because the pool
	//     opens with rec.EffectiveCredential(); if runtime_* is NULL
	//     that falls back to the owner cred, which bypasses RLS (see
	//     the loud warning above and the RUNTIME_PASSWORD_SECRET
	//     bootstrap contract in bootstrap.go). PR-D flips the flag +
	//     ships the isolation regression test.
	//
	//   * Console (platform-authenticated) traffic — routes
	//     unconditionally. Post-PR-A there IS no tenant schema on
	//     the platform DB for Team-tier projects, so the shared pool
	//     hard-fails with 3F000/42P01. The RLS-bypass concern is
	//     absent here: console traffic runs as service-role (see
	//     PlatformTenantContext setting KeyType="secret"), and
	//     applyRLSContext gives it the RLS-bypass branch by design.
	//     query.DeveloperRoleFromContext is the signal — set by
	//     PlatformTenantContext / PlatformStorageContext, never by
	//     APIKeyMiddleware.
	poolResolver := query.PoolResolver(func(ctx context.Context) *pgxpool.Pool {
		consoleTraffic := query.DeveloperRoleFromContext(ctx)
		if !consoleTraffic && !enableSDKRouting {
			return nil
		}
		pc, ok := auth.ProjectFromContext(ctx)
		if !ok || pc == nil || !pc.HasDedicatedDB {
			return nil
		}
		// Credential split matches the shared-cluster invariant:
		//   * Console (DeveloperRole) → OWNER pool. Tables created
		//     via the console SQL editor land as owner-owned, so
		//     runtime (gateway) traffic has RLS enforced against
		//     them. Console is already authorized as admin via
		//     RequireRole → RLS-bypass is intentional.
		//   * SDK (post-TEAM_TIER_ROUTING flip) → RUNTIME pool.
		//     Non-owner (eurobase_gateway) so RLS binds for end-user
		//     traffic — exactly what the loud warning above demands.
		var (
			p   *pgxpool.Pool
			err error
		)
		if consoleTraffic {
			p, err = poolCache.GetOwner(ctx, pc.ProjectID)
		} else {
			p, err = poolCache.Get(ctx, pc.ProjectID)
		}
		if err != nil {
			// Stale context (project_databases row deleted between
			// middleware and handler) or provider transient failure.
			// Fall back to shared pool rather than break the request.
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Error("pool cache: dedicated pool unavailable, falling back to shared",
					"project_id", pc.ProjectID, "error", err)
			}
			return nil
		}
		return p
	})

	// End-user auth service.
	endUserAuthSvc := enduser.NewAuthService(pool)
	if emailService != nil {
		endUserAuthSvc.SetEmailService(emailService)
	}
	if smsService != nil {
		endUserAuthSvc.SetSMSService(smsService)
	}
	if limiter != nil {
		// #227: wires the per-project hourly email + SMS quota checks
		// into the AuthService send paths. Nil is fine (dev without
		// Redis) — quotas just fail open.
		endUserAuthSvc.SetRateLimiter(limiter)
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
				limits := map[string]struct {
					limit  int
					window time.Duration
				}{
					"platform_signup":    {ratelimit.SignupLimit, ratelimit.SignupWindow},
					"platform_forgot":    {ratelimit.ForgotPasswordLimit, ratelimit.ForgotPasswordWindow},
					"signin_fail":        {ratelimit.SigninFailLimit, ratelimit.SigninFailWindow},
					"signin_fail_record": {ratelimit.SigninFailLimit, ratelimit.SigninFailWindow},
				}
				cfg, ok := limits[action]
				if !ok {
					cfg = struct {
						limit  int
						window time.Duration
					}{5, 15 * time.Minute}
				}
				return ratelimit.CheckAuthRate(limiter, w, r.Context(), action, identifier, cfg.limit, cfg.window)
			}
		}
		r.Post("/auth/signup", auth.HandlePlatformSignUp(platformAuthSvc, platformRateCheck))
		r.Post("/auth/signin", auth.HandlePlatformSignIn(platformAuthSvc, platformRateCheck))
		r.Post("/auth/forgot-password", auth.HandlePlatformForgotPassword(platformAuthSvc, platformRateCheck))
		r.Post("/auth/reset-password", auth.HandlePlatformResetPassword(platformAuthSvc))

		// Mailing opt-out — unauthenticated (possession of the
		// HMAC-signed token IS the authorisation). GET renders a
		// confirm form so mail scanners (Defender SafeLinks etc.)
		// pre-fetching the URL can't silently opt users out; POST
		// actually performs the write. Also supports RFC 8058
		// One-Click via POST-with-token-in-query. Phase C.
		if unsubSigner != nil {
			handler := email.UnsubscribeHandler(unsubSigner, pool)
			r.Get("/mailing/unsubscribe", handler)
			r.Post("/mailing/unsubscribe", handler)
		}

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

			// Mailing preferences — the in-console counterpart to
			// the drip-mail unsubscribe link. Categories match the
			// mailing_preferences CHECK constraint (migration 000077).
			r.Get("/mailing-preferences", auth.HandleListMailingPreferences(platformAuthSvc))
			r.Put("/mailing-preferences", auth.HandleSetMailingPreference(platformAuthSvc))
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

		// Authenticated: billing (Mollie subscription checkout).
		// Feature-flagged behind BILLING_ENABLED — the handler
		// returns 503 when the service reports disabled, so wiring
		// the route unconditionally is safe. Downstream PRs add
		// subscription management + invoice download.
		if billingSvc != nil {
			r.Route("/billing", func(r chi.Router) {
				if isDev {
					r.Use(devAuthMiddleware)
				} else {
					r.Use(platformAuth.Handler)
				}
				r.Post("/checkout", billing.HandleCreateCheckout(billingSvc))
				r.Get("/invoices", billing.HandleListInvoices(billingSvc))
				r.Get("/invoices/{id}/pdf", billing.HandleDownloadInvoicePDF(billingSvc))
				r.Post("/subscriptions/{id}/cancel", billing.HandleCancelSubscription(billingSvc))
				r.Get("/projects/{project_id}/subscription", billing.HandleGetProjectSubscription(billingSvc))
			})

			// UNAUTHENTICATED: Mollie's webhook endpoint. Mollie
			// doesn't sign webhook POSTs — trust rests on the URL
			// being a secret held only by us + Mollie plus the
			// handler GET-ing canonical state back from Mollie's
			// API. A malicious POST with a random ID either hits
			// 404 at Mollie (ErrNotFound → 200 no-op) or lands on
			// an idempotent state that's already applied.
			//
			// Deliberately registered OUTSIDE the platform auth
			// middleware — Mollie is not a Eurobase platform user.
			r.Post("/billing/webhook", billing.HandleMollieWebhook(billingSvc))
		}

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
			// Team-tier closed-beta grants (M2, issue #308).
			r.Get("/team-beta", tenant.AdminListTeamBetaUsers(pool))
			r.Post("/team-beta/{id}", tenant.AdminGrantTeamBeta(pool))
			r.Delete("/team-beta/{id}", tenant.AdminRevokeTeamBeta(pool))
			// Legal-Team-tier closed-beta grants (M2b).
			r.Get("/legal-team-beta", tenant.AdminListLegalTeamBetaUsers(pool))
			r.Post("/legal-team-beta/{id}", tenant.AdminGrantLegalTeamBeta(pool))
			r.Delete("/legal-team-beta/{id}", tenant.AdminRevokeLegalTeamBeta(pool))
			// Signup-users dashboard (public-beta launch observability).
			// One row per platform_users + derived plan / MRR / project count.
			r.Get("/signup-users", tenant.AdminListSignupUsers(pool))
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
			// DDL on tenant schemas.
			//
			// Free/Pro: the developer pool + SET LOCAL ROLE
			// eurobase_migrator gives DDL the migrator-owned-tables
			// invariant (matches CI-applied migrations); withDeveloperRole
			// carries the flag runDDL reads.
			//
			// Team-tier (PR-D): PlatformTenantContext stashes the
			// dedicated-instance OWNER pool in ctx via
			// query.ContextWithTenantPool. runDDL reads it first, so
			// the DDL runs against the dedicated instance as the
			// owner (no eurobase_migrator role exists there — the
			// role split is preserved by the *connection* role
			// instead of a SET LOCAL ROLE swap). PlatformTenantContext
			// also sets WithDeveloperRole, so withDeveloperRole
			// becomes redundant but kept for defence-in-depth if the
			// context middleware ever gets reordered.
			r.With(tenant.RequireMinRole("developer"), tenant.PlatformTenantContext(pool, tenantPoolResolver), withDeveloperRole).Mount("/schema/tables", query.HandleDDL(developerPool))
			r.With(tenant.RequireMinRole("developer"), tenant.PlatformTenantContext(pool, tenantPoolResolver), withDeveloperRole).Mount("/schema/functions", query.HandleFunctions(developerPool))
			// Tenant-level versioned migrations (#190). Platform auth +
			// developer role; each migration runs under a per-tenant LOGIN
			// role the gateway connects as (see MigrationExecutor), so a
			// malicious body can reach exactly one tenant. Deliberately NOT
			// on /v1: data-plane keys never run DDL. Fails closed (503) when
			// the executor isn't configured (no DDL_PASSWORD_SECRET).
			r.With(tenant.RequireMinRole("developer")).Mount("/migrations", query.HandleTenantMigrations(migrationExec, pool))
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

			// Per-project BYO custom SMTP sender (#235 Part 1). Admin-
			// only because the password is platform-trust-equivalent
			// to a bank account number — a viewer must not see the
			// has_password flag, let alone send a test through it.
			// The send-path consults this via emailService.senderSvc,
			// wired in main.go's NewEmailService chain.
			if vaultSvc != nil && vaultSvc.Configured() {
				senderSvc := email.NewSenderService(pool, vaultSvc)
				if emailService != nil {
					emailService.WithSenderService(senderSvc)
				}
				r.With(tenant.RequireMinRole("admin")).Get("/email-sender", email.HandleGetSender(senderSvc))
				r.With(tenant.RequireMinRole("admin")).Put("/email-sender", email.HandlePutSender(senderSvc))
				r.With(tenant.RequireMinRole("admin")).Delete("/email-sender", email.HandleDeleteSender(senderSvc))
				r.With(tenant.RequireMinRole("admin")).Post("/email-sender/test", email.HandleTestSender(senderSvc))
			}

			// Vault (encrypted secrets storage) — platform-authenticated.
			if vaultSvc != nil && vaultSvc.Configured() {
				r.With(tenant.RequireMinRole("admin")).Mount("/vault", vault.Routes(vaultSvc, pool))
			}

			// Compliance (DPA report, sub-processor registry, DSAR exports).
			//
			// Closes #173: the DPA report's data-flow encryption flags
			// now derive from RESIDENCY_REGION / ENCRYPTION_AT_REST /
			// TLS_MIN env vars, not from hardcoded `true`. Defaults
			// match production today, so a missing env var still
			// renders the report shipped configuration — not "unknown".
			residencyCfg := compliance.LoadResidencyConfigFromEnv()
			complianceSvc := compliance.NewComplianceService(pool, residencyCfg)
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

			// Team-tier backup + restore surface (M3).
			// Gated inside each handler on plans.CheckDedicatedDB —
			// 402 for Free/Pro. All routes admin-only in the
			// membership sense (destructive by nature).
			backupSvc := tenant.NewBackupService(pool, providerRegistry, limitsSvc)
			r.With(tenant.RequireMinRole("viewer")).Get("/backups", backupSvc.HandleListBackups())
			r.With(tenant.RequireMinRole("admin")).Post("/backups", backupSvc.HandleCreateBackup())
			r.With(tenant.RequireMinRole("admin")).Post("/restore", backupSvc.HandleCreateRestore())
			r.With(tenant.RequireMinRole("viewer")).Get("/restore/{restoreId}", backupSvc.HandleGetRestore())

			// Team-tier direct-DATABASE_URL surface (M4).
			// Emits a real postgres:// URL for Payload / Prisma /
			// Drizzle / psql — the whole reason a customer chooses
			// Team over Pro. Rotation lives on the same service so
			// a leaked URL is a one-click reset. Every URL fetch is
			// audited (ActionConnectionURLViewed) — the URL is a
			// bearer credential once revealed.
			if connCipher != nil {
				// Insert-only River client for the retry-provisioning
				// endpoint. Nil-safe on ConnectionService: if the
				// client fails to construct we log + degrade gracefully
				// (retry endpoint returns 503; every other Connection
				// tab route still works).
				var connRiverClient *river.Client[pgx.Tx]
				if rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{}); err == nil {
					connRiverClient = rc
				} else {
					slog.Warn("connection retry-provisioning disabled: river insert-only client init failed", "error", err)
				}
				connSvc := tenant.NewConnectionService(pool, providerRegistry, connCipher, limitsSvc).
					WithPoolCache(poolCache).
					WithRiverClient(connRiverClient)
				r.With(tenant.RequireMinRole("admin")).Get("/connection", connSvc.HandleGetConnection())
				r.With(tenant.RequireMinRole("viewer")).Get("/connection/state", connSvc.HandleGetConnectionState())
				r.With(tenant.RequireMinRole("admin")).Post("/connection/rotate", connSvc.HandleRotateConnection())
				r.With(tenant.RequireMinRole("admin")).Post("/connection/retry-provision", connSvc.HandleRetryProvisioning())
			}

			// Legal-Team compliance surface (M2b). Gated by
			// plans.CheckLegalTeamTier inside each handler — returns
			// 402 with code=legal_team_required for non-legal-team
			// projects so the console can render an upgrade prompt.
			holdSvc := compliance.NewHoldService(pool)
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/retention-holds", compliance.HandlePlaceRetentionHold(pool, limitsSvc, holdSvc))
			r.With(tenant.RequireMinRole("viewer")).Get("/compliance/retention-holds", compliance.HandleListRetentionHolds(pool, limitsSvc, holdSvc))
			r.With(tenant.RequireMinRole("admin")).Delete("/compliance/retention-holds/{holdId}", compliance.HandleRevokeRetentionHold(pool, limitsSvc, holdSvc))
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/gobd-export", compliance.HandleGoBDExport(pool, limitsSvc))

			// Storage retention policies (Legal-Team M2b follow-on, #330).
			// Per-prefix WORM defaults that the upload path resolves at
			// PUT time; Scaleway then enforces immutability at rest via
			// S3 Object Lock. The bucket-lock checker refuses new
			// policies against buckets that weren't provisioned with
			// Object Lock (guards tier-upgraded projects — a plain
			// bucket + lock headers on PUT would fail InvalidRequest).
			storageRetentionSvc := compliance.NewStorageRetentionService(pool)
			if s3Client != nil {
				storageRetentionSvc = storageRetentionSvc.WithBucketLockChecker(s3Client)
			}
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/storage-retention-policies", compliance.HandleUpsertStorageRetentionPolicy(pool, limitsSvc, storageRetentionSvc))
			r.With(tenant.RequireMinRole("viewer")).Get("/compliance/storage-retention-policies", compliance.HandleListStorageRetentionPolicies(pool, limitsSvc, storageRetentionSvc))
			r.With(tenant.RequireMinRole("admin")).Delete("/compliance/storage-retention-policies", compliance.HandleRemoveStorageRetentionPolicy(pool, limitsSvc, storageRetentionSvc))

			// SIEM export destinations (#353). Per-tenant sinks —
			// customer registers a webhook or syslog endpoint; the
			// deliverers (#354 / #355) forward each audit_log event.
			// Test endpoint returns 501 until at least one deliverer
			// ships (routes stay registered so console renders
			// "Test (coming soon)" not "not found").
			destSvc := compliance.NewDestinationService(pool)
			// Webhook + syslog deliverers for the Test endpoint
			// (#354 / #355). Same code paths as the scheduled
			// deliverer workers — one implementation per kind.
			webhookDeliverer := export.NewDeliverer(export.ClientConfig{})
			testDelivererAdapter := &webhookTestAdapter{d: webhookDeliverer}
			syslogTestAdapter := &syslogTestAdapter{d: export.NewSyslogDeliverer()}
			// Vault lookup shim for the Test path — closes over
			// vaultSvc without leaking a *vault.VaultService through
			// the compliance package.
			var vaultLookup func(ctx context.Context, schemaName, name string) (string, error)
			if vaultSvc != nil {
				vaultLookup = func(ctx context.Context, schemaName, name string) (string, error) {
					sec, err := vaultSvc.Get(ctx, schemaName, name)
					if err != nil {
						return "", err
					}
					return sec.Value, nil
				}
			}
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/audit-export", compliance.HandleCreateDestination(pool, limitsSvc, destSvc, auditSvc))
			r.With(tenant.RequireMinRole("viewer")).Get("/compliance/audit-export", compliance.HandleListDestinations(pool, limitsSvc, destSvc))
			r.With(tenant.RequireMinRole("admin")).Patch("/compliance/audit-export/{destID}", compliance.HandleUpdateDestination(pool, limitsSvc, destSvc, auditSvc))
			r.With(tenant.RequireMinRole("admin")).Delete("/compliance/audit-export/{destID}", compliance.HandleRemoveDestination(pool, limitsSvc, destSvc, auditSvc))
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/audit-export/{destID}/test", compliance.HandleTestDestination(pool, limitsSvc, destSvc, testDelivererAdapter, syslogTestAdapter, vaultLookup))

			// Breach register (Tier-1 #4, closes #172). Append-only by
			// migration 000065. Admin-only because the register names
			// affected subjects and triggers customer/authority comms.
			// DPO_EMAIL drives the "point of contact" in templates;
			// defaults to dpo@eurobase.app if unset.
			dpoEmail := os.Getenv("DPO_EMAIL")
			if dpoEmail == "" {
				dpoEmail = "dpo@eurobase.app"
			}
			var breachMailer breach.Mailer
			if emailService != nil {
				breachMailer = &breach.MailerAdapter{Svc: emailService}
			}
			breachSvc := breach.NewService(pool, auditSvc, metricsReg)
			breachH := &breach.Handler{Svc: breachSvc, Pool: pool, Mailer: breachMailer, DPOEmail: dpoEmail}
			r.With(tenant.RequireMinRole("admin")).Get("/compliance/breaches", breachH.HandleList())
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/breaches", breachH.HandleOpen())
			r.With(tenant.RequireMinRole("admin")).Get("/compliance/breaches/{incidentId}", breachH.HandleGet())
			r.With(tenant.RequireMinRole("admin")).Patch("/compliance/breaches/{incidentId}", breachH.HandleUpdate())
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/breaches/{incidentId}/close", breachH.HandleClose())
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/breaches/{incidentId}/subjects", breachH.HandleSubjects())
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/breaches/{incidentId}/notify-customers", breachH.HandleNotifyCustomers())
			r.With(tenant.RequireMinRole("admin")).Post("/compliance/breaches/{incidentId}/authority-form", breachH.HandleAuthorityForm())
			r.With(tenant.RequireMinRole("admin")).Get("/compliance/breaches/{incidentId}/sla", breachH.HandleSLAStatus())

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
			fnSvc := functions.NewService(pool, vaultSvc)
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
				r.Use(tenant.PlatformTenantContext(pool, tenantPoolResolver))
				r.Mount("/", enduser.PlatformRoutes(pool, enduserPoolResolver, limiter))
			})

			// Console storage proxy — platform-authenticated access to project storage.
			// Reads → viewer; uploads/deletes → developer (closes #50).
			// We bypass storageHandler.Routes() here so each method gets
			// its own gate; the SDK mount keeps using Routes() unchanged.
			if s3Client != nil {
				r.Route("/storage", func(r chi.Router) {
					r.Use(tenant.PlatformStorageContext(pool))
					// Console storage: route metadata reads/writes to
					// the dedicated instance for Team-tier. The inner
					// QueryEngine gets the same resolver so ownership
					// checks (assertObjectVisible) also route.
					storageHandler := storage.NewStorageHandler(s3Client, pool, query.NewQueryEngine(pool).WithPoolResolver(poolResolver)).
						WithRetentionResolver(compliance.NewStorageRetentionService(pool)).
						WithHoldChecker(compliance.NewHoldService(pool)).
						WithPoolResolver(storage.PoolResolver(enduserPoolResolver))
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
				r.Use(tenant.PlatformTenantContext(pool, tenantPoolResolver))

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
		// Personal-data access logging (Tier-1 GDPR #4). Stash the recorder and
		// the resolved client IP in the request context so the db/storage/auth
		// handlers below can record reads/exports/downloads without new
		// constructor params. Runs before the auth middleware (which only
		// populate project/end-user context) — both values here are available
		// immediately; handlers read project/user from context at record time.
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := audit.WithAccessRecorder(r.Context(), accessRecorder)
				ctx = audit.WithClientIP(ctx, ratelimit.ClientIP(r))
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

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
			r.Post("/refresh", enduser.HandleRefresh(endUserAuthSvc, limiter))
			r.Post("/signout", enduser.HandleSignOut(endUserAuthSvc))
			r.Post("/forgot-password", enduser.HandleForgotPassword(endUserAuthSvc, limiter))
			r.Post("/reset-password", enduser.HandleResetPassword(endUserAuthSvc, limiter))
			r.Post("/verify-email", enduser.HandleVerifyEmail(endUserAuthSvc, limiter))
			r.Post("/resend-verification", enduser.HandleResendVerification(endUserAuthSvc, limiter))
			r.Post("/request-magic-link", enduser.HandleRequestMagicLink(endUserAuthSvc, limiter))
			r.Post("/signin-magic-link", enduser.HandleSignInWithMagicLink(endUserAuthSvc, limiter))
			r.Get("/oauth/{provider}", enduser.HandleOAuthRedirect(endUserAuthSvc))
			r.Post("/phone/send-otp", enduser.HandleSendPhoneOTP(endUserAuthSvc, limiter))
			r.Post("/phone/verify", enduser.HandleVerifyPhoneOTP(endUserAuthSvc, limiter))

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

			// SDK runtime engine — routes tenant queries to the
			// dedicated pool when the API key resolves to a Team+
			// project with HasDedicatedDB=true (Team-tier M2.5).
			queryEngine := query.NewQueryEngine(pool).WithPoolResolver(poolResolver)
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

				// SDK storage — same routing story as SDK SQL: tenant
				// metadata lookups go to the dedicated instance when the
				// project has one (M2.5). Legal-Team projects get a
				// retention resolver so per-prefix WORM policies apply
				// to SDK uploads too (not just console uploads), plus a
				// hold checker so post-upload retention_holds refuse
				// SDK deletes with 409 object_locked.
				storageHandler := storage.NewStorageHandler(s3Client, pool, query.NewQueryEngine(pool).WithPoolResolver(poolResolver)).
					WithRetentionResolver(compliance.NewStorageRetentionService(pool)).
					WithHoldChecker(compliance.NewHoldService(pool))
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
		sdkFnSvc := functions.NewService(pool, vaultSvc)
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

// webhookTestAdapter bridges *export.Deliverer to
// compliance.TestDeliverer. Kept trivial so the compliance package
// doesn't need to import audit/export (the interface stays there so
// the concrete deliverer can move / change without touching the
// handler signature).
type webhookTestAdapter struct {
	d *export.Deliverer
}

func (a *webhookTestAdapter) PostEnvelope(ctx context.Context, endpoint string, secret []byte, body any) (int, error) {
	// Marshal here rather than in the deliverer — the test path
	// hands over an ad-hoc map[string]any (synthetic event), the
	// scheduled path hands over a typed *export.Envelope, and the
	// deliverer's PostEnvelope wants a typed *Envelope. Round-trip
	// through JSON keeps that boundary clean for the test path.
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	var env export.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, err
	}
	res := a.d.PostEnvelope(ctx, endpoint, secret, &env)
	return res.StatusCode, res.Err
}

// syslogTestAdapter bridges *export.SyslogDeliverer to
// compliance.SyslogTestDeliverer. Parses the vault-stored PEM
// bundle into a tls.Certificate (kept out of compliance to avoid
// the crypto/tls import there) and dials once with a single
// synthetic event.
type syslogTestAdapter struct {
	d *export.SyslogDeliverer
}

func (a *syslogTestAdapter) SendTestEvent(ctx context.Context, endpoint string, pemBundle []byte, action string, projectID string) error {
	var cert *tls.Certificate
	if len(pemBundle) > 0 {
		c, err := loadTLSCertFromRouterPEM(pemBundle)
		if err != nil {
			return fmt.Errorf("parse client cert: %w", err)
		}
		cert = &c
	}
	events := []export.SyslogEvent{
		{
			ID:        "00000000-0000-0000-0000-000000000000",
			ProjectID: projectID,
			Action:    action,
			CreatedAt: time.Now().UTC().Truncate(time.Second),
			Seq:       0,
		},
	}
	_, err := a.d.DialAndSend(ctx, endpoint, cert, events)
	return err
}

// loadTLSCertFromRouterPEM mirrors the worker's PEM parser —
// duplicated (rather than exported from workers) because the
// worker package imports internal/gateway indirectly via chains
// we don't want to invert. Tiny function; keeping both copies
// in lockstep is cheaper than the import gymnastics.
func loadTLSCertFromRouterPEM(bundle []byte) (tls.Certificate, error) {
	var certPEM, keyPEM []byte
	rest := bundle
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			certPEM = append(certPEM, pem.EncodeToMemory(block)...)
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			keyPEM = pem.EncodeToMemory(block)
		}
		rest = remaining
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return tls.Certificate{}, fmt.Errorf("bundle must contain both CERTIFICATE and PRIVATE KEY blocks")
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}
