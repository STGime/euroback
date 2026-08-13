package tenant

import (
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/go-chi/chi/v5"
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
			ctx = query.ContextWithProjectID(ctx, pc.ProjectID)
			ctx = query.ContextWithKeyType(ctx, pc.KeyType)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PlatformTenantContext resolves the tenant project from a chi URL param {id}
// and the platform auth claims. Used by the console's platform-authenticated
// data routes so the console never needs an API key.
func PlatformTenantContext(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.ClaimsFromContext(r.Context())
			if !ok {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			projectID := chi.URLParam(r, "id")
			if projectID == "" {
				http.Error(w, `{"error":"missing project id"}`, http.StatusBadRequest)
				return
			}

			// Check membership (any role grants read access at the schema level).
			role, roleErr := ResolveRole(r.Context(), pool, projectID, claims.Subject)
			if roleErr != nil || role == "" {
				slog.Error("platform tenant context: no membership",
					"project_id", projectID,
					"user_id", claims.Subject,
				)
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}

			// Load enough project metadata to populate a full
			// ProjectContext, so the router's poolResolver can route
			// console tenant-schema reads to the dedicated instance
			// for Team-tier projects (PR-C). The LEFT JOIN keeps this
			// a single round-trip; a Free/Pro project simply gets
			// HasDedicatedDB=false / ProjectDatabaseID=NULL and the
			// resolver falls back to the shared pool.
			var (
				schemaName string
				plan       string
				pdID       *string
			)
			err := pool.QueryRow(r.Context(),
				`SELECT p.schema_name, p.plan, pd.id
				   FROM projects p
				   LEFT JOIN public.project_databases pd
				     ON pd.project_id = p.id
				    AND pd.state IN ('provisioning', 'active', 'restoring')
				    AND pd.deleted_at IS NULL
				  WHERE p.id = $1 AND p.status = 'active'
				  ORDER BY (pd.state = 'active') DESC NULLS LAST, pd.created_at DESC NULLS LAST
				  LIMIT 1`,
				projectID,
			).Scan(&schemaName, &plan, &pdID)
			if err != nil {
				slog.Error("platform tenant context: project not found",
					"error", err,
					"project_id", projectID,
				)
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}

			ctx := query.ContextWithSchema(r.Context(), schemaName)
			ctx = query.ContextWithProjectID(ctx, projectID)
			// Console operates with "secret" level access.
			ctx = query.ContextWithKeyType(ctx, "secret")
			// Platform-authenticated developer traffic: the engine elevates
			// to eurobase_migrator inside the tx so DDL on tenant schemas
			// works against migrator-owned tables and new objects are
			// owned by the migrator (uniform with CI-applied migrations).
			ctx = query.WithDeveloperRole(ctx)
			// ProjectContext lets the router's poolResolver route
			// tenant-schema reads to the dedicated instance for
			// Team-tier projects. Post-PR-A there is no tenant schema
			// on the platform DB for Team-tier, so this MUST route to
			// avoid a hard 3F000/42P01 in the console.
			ctx = auth.ContextWithProject(ctx, &auth.ProjectContext{
				ProjectID:         projectID,
				SchemaName:        schemaName,
				Plan:              plan,
				KeyType:           "secret",
				HasDedicatedDB:    pdID != nil,
				ProjectDatabaseID: pdID,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PlatformStorageContext resolves the project slug from URL param {id} and
// platform auth claims, then injects X-Project-Slug into the request header
// so the existing storage handler can derive the bucket name.
func PlatformStorageContext(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.ClaimsFromContext(r.Context())
			if !ok {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			projectID := chi.URLParam(r, "id")
			if projectID == "" {
				http.Error(w, `{"error":"missing project id"}`, http.StatusBadRequest)
				return
			}

			// Check membership (any role grants storage access at the project level).
			role, roleErr := ResolveRole(r.Context(), pool, projectID, claims.Subject)
			if roleErr != nil || role == "" {
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}

			// Single round-trip: fetch slug + schema + plan + the live
			// project_databases id so the ProjectContext downstream
			// handlers see is Team-tier-routing complete. Without
			// HasDedicatedDB / ProjectDatabaseID the router's
			// poolResolver would refuse to route (assertObjectVisible,
			// storage_objects SELECTs, etc.) and every Team-tier
			// download / delete / signed-URL would 42P01 on the
			// shared pool.
			var (
				slug, schema, plan string
				pdID               *string
			)
			err := pool.QueryRow(r.Context(),
				`SELECT p.slug, p.schema_name, p.plan, pd.id
				   FROM projects p
				   LEFT JOIN public.project_databases pd
				     ON pd.project_id = p.id
				    AND pd.state IN ('provisioning', 'active', 'restoring')
				    AND pd.deleted_at IS NULL
				  WHERE p.id = $1 AND p.status = 'active'
				  ORDER BY (pd.state = 'active') DESC NULLS LAST, pd.created_at DESC NULLS LAST
				  LIMIT 1`,
				projectID,
			).Scan(&slug, &schema, &plan, &pdID)
			if err != nil {
				slog.Error("platform storage context: project not found",
					"error", err,
					"project_id", projectID,
					"user_id", claims.Subject,
				)
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}

			// Storage handlers read slug + schema from the authenticated
			// ProjectContext only. The X-Project-Slug header is no longer
			// trusted (advisory GHSA-gvrg-vq6j-j647).
			ctx := auth.ContextWithProject(r.Context(), &auth.ProjectContext{
				ProjectID:         projectID,
				SchemaName:        schema,
				Slug:              slug,
				Plan:              plan,
				KeyType:           "secret",
				HasDedicatedDB:    pdID != nil,
				ProjectDatabaseID: pdID,
			})
			// Console traffic operates with service-role access — the
			// admin is authorised against this project via ResolveRole
			// above. Without this, applyRLSContext defaults to
			// role='anon' and assertObjectVisible's storage_objects
			// SELECT is filtered to nothing, so the console returned
			// 404 on every download/delete (closes #87 second half).
			ctx = query.ContextWithKeyType(ctx, "secret")
			// WithDeveloperRole is the signal the router's
			// poolResolver uses to route console traffic to the
			// dedicated instance (owner pool). Without it,
			// h.engine.WithTenantTx inside assertObjectVisible would
			// stay on the shared pool → 42P01 for Team-tier. On the
			// shared pool the SET LOCAL ROLE eurobase_migrator is a
			// no-op because storage handlers don't do DDL — the
			// service-role marker above is what grants access.
			ctx = query.WithDeveloperRole(ctx)
			// Also stash the schema + project_id in the query context
			// so h.tenantPool(ctx) inside StorageHandler.WithPoolResolver
			// picks the dedicated pool via ProjectContext (which it
			// already does — belt + suspenders in case a future
			// change swaps the lookup order).
			ctx = query.ContextWithSchema(ctx, schema)
			ctx = query.ContextWithProjectID(ctx, projectID)
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
				// Fall back to user's first active project (via membership).
				err := pool.QueryRow(r.Context(),
					`SELECT p.id, p.schema_name
					 FROM projects p
					 JOIN project_members pm ON pm.project_id = p.id
					 WHERE pm.user_id = $1::uuid AND p.status = 'active'
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
			ctx = query.ContextWithProjectID(ctx, projectID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
