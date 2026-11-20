package enduser

import (
	"context"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSecretKeyRequiredMessage is the 403 answer of the user admin API to
// any key other than the project's secret key.
const ErrSecretKeyRequiredMessage = "the user admin API needs the project's secret key (eb_sk_...): call it from your server, never from a browser or an app"

// AdminRoutes is the SDK user admin API, /v1/auth/admin/users: the
// console's end-user management (same handlers, same validation) for a
// developer's server, authorised by the project's secret key.
//
// The router mounts it after the API-key middleware (project resolved,
// status active), the maintenance middleware and the SDK tenant-pool
// middleware (a Team project's runtime pool on the request, or 503). It
// reads the project from the API key alone: an end-user token on the
// request plays no part. Every query runs as the service role on the
// pool on the request, or the shared pool for a project without a
// dedicated database — never the shared cluster for one with.
//
// limiter (may be nil) caps password hashing per project (create with a
// password, set a password), as on the console.
func AdminRoutes(pool *pgxpool.Pool, limiter *ratelimit.RateLimiter) chi.Router {
	resolver := PoolResolver(func(ctx context.Context, _ string) *pgxpool.Pool {
		return query.TenantPoolFromContext(ctx)
	})
	r := chi.NewRouter()
	r.Use(RequireSecretKey)
	r.Use(adminTenantContext)
	r.Get("/", handleListUsers(pool, resolver))
	r.Post("/", handleCreateUser(pool, resolver, limiter))
	r.Route("/{userId}", func(r chi.Router) {
		r.Use(requireUserID)
		r.Get("/", handleGetUser(pool, resolver))
		r.Patch("/", handleUpdateUser(pool, resolver))
		r.Delete("/", handleDeleteUser(pool, resolver))
		r.Post("/suspend", handleSuspendUser(pool, resolver))
		r.Delete("/suspend", handleUnsuspendUser(pool, resolver))
		r.Post("/password", handleResetPassword(pool, resolver, limiter))
		r.Post("/signout", handleSignOutUser(pool, resolver))
	})
	return r
}

// RequireSecretKey refuses (403) every request whose API key isn't the
// project's secret key. Public keys ship in browsers and apps; the admin
// API reads and changes every account of the project.
//
// The key must come in the apikey header: the API-key middleware also
// accepts ?apikey= (for browser redirects), but a secret key in a URL
// ends up in access logs, browser history and proxies. The middleware
// reads the header first and the query only when the header is empty, so
// an empty header means the key came from the URL.
func RequireSecretKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok || pc == nil {
			writeJSON(w, map[string]string{"error": "missing project context"}, http.StatusUnauthorized)
			return
		}
		if pc.KeyType != "secret" {
			writeJSON(w, map[string]string{"error": ErrSecretKeyRequiredMessage, "code": "secret_key_required"}, http.StatusForbidden)
			return
		}
		if r.Header.Get("apikey") == "" {
			writeJSON(w, map[string]string{"error": "send the secret key in the apikey header, not in the URL", "code": "apikey_header_required"}, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminTenantContext puts the API key's project on the query context
// (schema, project id, key type) the shared handlers read, and refuses
// (503) a project with a dedicated database whose pool isn't on the
// request — the handlers would otherwise use the shared cluster.
func adminTenantContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok || pc == nil || pc.SchemaName == "" {
			writeJSON(w, map[string]string{"error": "missing project context"}, http.StatusUnauthorized)
			return
		}
		ctx := r.Context()
		if (pc.HasDedicatedDB || query.HasDedicatedDBFromContext(ctx)) && query.TenantPoolFromContext(ctx) == nil {
			writeJSON(w, map[string]string{"error": "the project's dedicated database is not available right now"}, http.StatusServiceUnavailable)
			return
		}
		ctx = query.ContextWithSchema(ctx, pc.SchemaName)
		ctx = query.ContextWithProjectID(ctx, pc.ProjectID)
		ctx = query.ContextWithKeyType(ctx, pc.KeyType)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
