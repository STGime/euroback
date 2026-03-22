package tenant

import (
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantContextFromProject returns middleware that reads the ProjectContext
// (set by APIKeyMiddleware) and stores the schema name in the request context.
// This is used for SDK routes where the project is identified by API key.
func TenantContextFromProject() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pc, ok := auth.ProjectFromContext(r.Context())
			if !ok {
				slog.Warn("tenant context from project: no project context")
				http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
				return
			}

			ctx := query.ContextWithSchema(r.Context(), pc.SchemaName)
			ctx = query.ContextWithKeyType(ctx, pc.KeyType)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantContextMiddleware resolves the tenant project and stores the schema
// name and project ID in the request context for downstream handlers.
//
// Project resolution order:
// 1. X-Project-Id header (explicit, used by the console)
// 2. Fall back to the user's first active project
func TenantContextMiddleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.ClaimsFromContext(r.Context())
			if !ok {
				slog.Warn("tenant context middleware: no auth claims in context")
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			var schemaName string
			var projectID string

			// If X-Project-Id is provided, resolve that specific project
			// and verify the authenticated user owns it.
			if headerProjectID := r.Header.Get("X-Project-Id"); headerProjectID != "" {
				err := pool.QueryRow(r.Context(),
					`SELECT p.id, p.schema_name
					 FROM projects p
					 WHERE p.id = $1 AND p.owner_id = $2::uuid AND p.status = 'active'`,
					headerProjectID, claims.Subject,
				).Scan(&projectID, &schemaName)
				if err != nil {
					slog.Error("tenant context: project not found or not owned by user",
						"error", err,
						"project_id", headerProjectID,
						"user_id", claims.Subject,
					)
					http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
					return
				}
			} else {
				// Fall back to user's first active project.
				err := pool.QueryRow(r.Context(),
					`SELECT p.id, p.schema_name
					 FROM projects p
					 WHERE p.owner_id = $1::uuid AND p.status = 'active'
					 ORDER BY p.created_at ASC
					 LIMIT 1`,
					claims.Subject,
				).Scan(&projectID, &schemaName)
				if err != nil {
					slog.Error("tenant context: failed to resolve project",
						"error", err,
						"user_id", claims.Subject,
					)
					http.Error(w, `{"error":"no active project found"}`, http.StatusNotFound)
					return
				}
			}

			slog.Debug("tenant context established",
				"user_id", claims.Subject,
				"project_id", projectID,
				"schema", schemaName,
			)

			ctx := query.ContextWithSchema(r.Context(), schemaName)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
