package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SubdomainMiddleware extracts the project slug from the Host header
// (e.g. "my-app.eurobase.app" → "my-app") and resolves the project context.
// This allows SDK requests to reach projects via their subdomain URL
// without needing an API key for project identification — though an API key
// is still required for authentication.
type SubdomainMiddleware struct {
	pool   *pgxpool.Pool
	suffix string // e.g. ".eurobase.app"
}

// NewSubdomainMiddleware creates middleware that resolves projects by subdomain.
// suffix is the domain suffix to strip (e.g. ".eurobase.app").
func NewSubdomainMiddleware(pool *pgxpool.Pool, suffix string) *SubdomainMiddleware {
	if !strings.HasPrefix(suffix, ".") {
		suffix = "." + suffix
	}
	return &SubdomainMiddleware{pool: pool, suffix: suffix}
}

// Handler is the chi-compatible middleware func.
// It extracts the slug from the Host header, resolves the project,
// and injects the ProjectContext. The API key middleware still runs
// after this to authenticate the request — this middleware only narrows
// which project the request targets.
func (m *SubdomainMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// Strip port if present (e.g. "my-app.eurobase.app:8080").
		if idx := strings.LastIndex(host, ":"); idx != -1 {
			host = host[:idx]
		}

		if !strings.HasSuffix(host, m.suffix) {
			// Not a subdomain request — pass through (handled by other routes).
			next.ServeHTTP(w, r)
			return
		}

		slug := strings.TrimSuffix(host, m.suffix)
		if slug == "" || slug == "api" || slug == "console" {
			// Reserved subdomains — pass through.
			next.ServeHTTP(w, r)
			return
		}

		var pc ProjectContext
		err := m.pool.QueryRow(r.Context(),
			`SELECT id, schema_name, jwt_secret, auth_config
			 FROM projects
			 WHERE slug = $1 AND status = 'active'`,
			slug,
		).Scan(&pc.ProjectID, &pc.SchemaName, &pc.JWTSecret, &pc.AuthConfig)
		if err != nil {
			slog.Warn("subdomain project not found", "slug", slug, "error", err)
			http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
			return
		}

		slog.Debug("subdomain resolved", "slug", slug, "project_id", pc.ProjectID)

		// Inject the project context. The API key middleware will still
		// validate the apikey header and set KeyType.
		ctx := ContextWithProject(r.Context(), &pc)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
