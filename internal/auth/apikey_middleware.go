package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// APIKeyMiddleware validates the apikey header and resolves the project context.
type APIKeyMiddleware struct {
	pool *pgxpool.Pool
}

// NewAPIKeyMiddleware creates a new API key validation middleware.
func NewAPIKeyMiddleware(pool *pgxpool.Pool) *APIKeyMiddleware {
	return &APIKeyMiddleware{pool: pool}
}

// Handler is the chi-compatible middleware func.
func (m *APIKeyMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("apikey")
		if apiKey == "" {
			// Fall back to query parameter for browser-initiated flows (e.g. OAuth redirects).
			apiKey = r.URL.Query().Get("apikey")
		}
		if apiKey == "" {
			http.Error(w, `{"error":"missing apikey header"}`, http.StatusUnauthorized)
			return
		}

		// SHA-256 hash the key to look up in the database.
		h := sha256.Sum256([]byte(apiKey))
		keyHash := hex.EncodeToString(h[:])

		var pc ProjectContext
		err := m.pool.QueryRow(r.Context(),
			`SELECT p.id, p.schema_name, p.jwt_secret, ak.type, COALESCE(p.plan, 'free'), p.auth_config
			 FROM api_keys ak
			 JOIN projects p ON ak.project_id = p.id
			 WHERE ak.key_hash = $1 AND p.status = 'active'`,
			keyHash,
		).Scan(&pc.ProjectID, &pc.SchemaName, &pc.JWTSecret, &pc.KeyType, &pc.Plan, &pc.AuthConfig)
		if err != nil {
			slog.Warn("invalid API key", "error", err, "prefix", safePrefix(apiKey))
			http.Error(w, `{"error":"invalid API key"}`, http.StatusUnauthorized)
			return
		}

		// If a subdomain already resolved a project, verify the API key
		// belongs to the same project.
		if existing, ok := ProjectFromContext(r.Context()); ok {
			if existing.ProjectID != pc.ProjectID {
				slog.Warn("API key does not match subdomain project",
					"subdomain_project", existing.ProjectID,
					"apikey_project", pc.ProjectID,
				)
				http.Error(w, `{"error":"API key does not belong to this project"}`, http.StatusUnauthorized)
				return
			}
		}

		// Update last_used_at (fire and forget).
		go func() {
			_, _ = m.pool.Exec(r.Context(),
				`UPDATE api_keys SET last_used_at = now() WHERE key_hash = $1`,
				keyHash,
			)
		}()

		slog.Debug("API key validated",
			"project_id", pc.ProjectID,
			"key_type", pc.KeyType,
		)

		ctx := ContextWithProject(r.Context(), &pc)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func safePrefix(key string) string {
	if len(key) > 14 {
		return key[:14] + "..."
	}
	return "***"
}
