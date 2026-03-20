package tenant

import (
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantContextMiddleware extracts the authenticated user's default project
// and stores the tenant schema name in the request context. It also calls
// set_tenant_id() on the connection to activate RLS policies.
func TenantContextMiddleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.ClaimsFromContext(r.Context())
			if !ok {
				slog.Warn("tenant context middleware: no auth claims in context")
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			// Look up the user's default (first) project.
			var schemaName string
			var projectID string
			err := pool.QueryRow(r.Context(),
				`SELECT p.id, p.schema_name
				 FROM projects p
				 JOIN platform_users u ON p.owner_id = u.id
				 WHERE u.hanko_user_id = $1 AND p.status = 'active'
				 ORDER BY p.created_at ASC
				 LIMIT 1`,
				claims.Subject,
			).Scan(&projectID, &schemaName)
			if err != nil {
				slog.Error("tenant context: failed to resolve project",
					"error", err,
					"hanko_user_id", claims.Subject,
				)
				http.Error(w, `{"error":"no active project found"}`, http.StatusNotFound)
				return
			}

			// Acquire a connection and call set_tenant_id for RLS.
			conn, err := pool.Acquire(r.Context())
			if err != nil {
				slog.Error("tenant context: failed to acquire connection", "error", err)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
				return
			}
			defer conn.Release()

			_, err = conn.Exec(r.Context(), "SELECT public.set_tenant_id($1::uuid)", projectID)
			if err != nil {
				slog.Error("tenant context: set_tenant_id failed",
					"error", err,
					"project_id", projectID,
				)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
				return
			}

			slog.Debug("tenant context established",
				"hanko_user_id", claims.Subject,
				"project_id", projectID,
				"schema", schemaName,
			)

			// Store schema name in context for downstream handlers.
			ctx := query.ContextWithSchema(r.Context(), schemaName)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
