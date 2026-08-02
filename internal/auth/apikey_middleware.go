package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// ResolveAPIKey looks up an API key value and returns the project
// context. Exported so non-middleware code paths (the realtime
// WebSocket authorize callback in #62) can validate apikeys without
// the middleware glue. Returns an error if the key is unknown, the
// project is not active, or the lookup fails.
//
// Team-tier M2: also LEFT JOINs project_databases so callers know
// whether the project has a dedicated managed-PG instance. The
// pd_id column is nullable in the SELECT — populated only for
// projects with a live row (state in provisioning/active/restoring,
// deleted_at IS NULL).
func ResolveAPIKey(ctx context.Context, pool *pgxpool.Pool, apiKey string) (*ProjectContext, error) {
	if apiKey == "" {
		return nil, errInvalidAPIKey
	}
	h := sha256.Sum256([]byte(apiKey))
	keyHash := hex.EncodeToString(h[:])

	var pc ProjectContext
	var pdID *string
	err := pool.QueryRow(ctx,
		`SELECT p.id, p.schema_name, p.slug, p.jwt_secret, ak.type,
		        COALESCE(p.plan, 'free'), p.auth_config,
		        pd.id
		   FROM api_keys ak
		   JOIN projects p ON ak.project_id = p.id
		   LEFT JOIN project_databases pd
		          ON pd.project_id = p.id
		         AND pd.state IN ('provisioning','active','restoring')
		         AND pd.deleted_at IS NULL
		  WHERE ak.key_hash = $1 AND p.status = 'active'`,
		keyHash,
	).Scan(&pc.ProjectID, &pc.SchemaName, &pc.Slug, &pc.JWTSecret, &pc.KeyType,
		&pc.Plan, &pc.AuthConfig, &pdID)
	if err != nil {
		return nil, errInvalidAPIKey
	}
	if pdID != nil {
		pc.ProjectDatabaseID = pdID
		pc.HasDedicatedDB = true
	}
	return &pc, nil
}

var errInvalidAPIKey = errors.New("invalid API key")

// ErrInvalidAPIKey is returned by ResolveAPIKey when the key is
// unknown or the project is inactive. Callers can compare against
// this to distinguish "bad key" from transient DB errors (though
// today the lookup always returns this on any failure).
var ErrInvalidAPIKey = errInvalidAPIKey

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
		var pdID *string
		err := m.pool.QueryRow(r.Context(),
			`SELECT p.id, p.schema_name, p.slug, p.jwt_secret, ak.type,
			        COALESCE(p.plan, 'free'), p.auth_config,
			        pd.id
			   FROM api_keys ak
			   JOIN projects p ON ak.project_id = p.id
			   LEFT JOIN project_databases pd
			          ON pd.project_id = p.id
			         AND pd.state IN ('provisioning','active','restoring')
			         AND pd.deleted_at IS NULL
			  WHERE ak.key_hash = $1 AND p.status = 'active'`,
			keyHash,
		).Scan(&pc.ProjectID, &pc.SchemaName, &pc.Slug, &pc.JWTSecret, &pc.KeyType,
			&pc.Plan, &pc.AuthConfig, &pdID)
		if err != nil {
			slog.Warn("invalid API key", "error", err, "prefix", safePrefix(apiKey))
			http.Error(w, `{"error":"invalid API key"}`, http.StatusUnauthorized)
			return
		}
		if pdID != nil {
			pc.ProjectDatabaseID = pdID
			pc.HasDedicatedDB = true
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
		// Closes #63: r.Context() is canceled when the response is
		// written, so the goroutine raced with cancellation and the
		// UPDATE silently dropped on fast handlers. context.WithoutCancel
		// preserves the values (request id, audit actor) but detaches
		// from the cancel signal.
		bgCtx := context.WithoutCancel(r.Context())
		go func() {
			_, _ = m.pool.Exec(bgCtx,
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

// safePrefix returns a key fragment safe to log: the literal prefix
// (eb_pk_ / eb_sk_) plus 6 hex chars of entropy, which is enough to
// disambiguate two keys per project visually but not enough to be
// useful in offline brute-force.
//
// Closes #59 — the previous 14-char window leaked ~32 bits of key
// entropy into log pipelines.
func safePrefix(key string) string {
	const window = 12 // 6-char prefix + 6 hex chars
	if len(key) > window {
		return key[:window] + "..."
	}
	return "***"
}
