package tenant

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// APIKeyResponse represents an API key in list responses (never includes the full key).
type APIKeyResponse struct {
	ID        string     `json:"id"`
	KeyPrefix string     `json:"key_prefix"`
	Type      string     `json:"type"`
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used_at"`
	// PublicKey is the full public key (public-type rows only; empty until
	// known for keys created before migration 000132 and not used since).
	PublicKey string `json:"public_key,omitempty"`
}

// APIKeyCreatedResponse includes the full plaintext keys. The secret key is
// shown only here; the public key stays retrievable via HandleListAPIKeys.
type APIKeyCreatedResponse struct {
	PublicKey string `json:"public_key"`
	SecretKey string `json:"secret_key"`
}

// HandleListAPIKeys returns the API keys for a project: prefixes, plus the
// full public key (it's public by design). Secret keys never come back.
func HandleListAPIKeys(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		rows, err := pool.Query(r.Context(),
			`SELECT id, key_prefix, type, created_at, last_used_at,
			        CASE WHEN type = 'public' THEN coalesce(public_key, '') ELSE '' END
			 FROM api_keys WHERE project_id = $1 ORDER BY created_at ASC`, projectID)
		if err != nil {
			slog.Error("list api keys failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		keys := make([]APIKeyResponse, 0)
		for rows.Next() {
			var k APIKeyResponse
			if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.Type, &k.CreatedAt, &k.LastUsed, &k.PublicKey); err != nil {
				slog.Error("scan api key failed", "error", err)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
				return
			}
			keys = append(keys, k)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(keys)
	}
}

// HandleRegenerateAPIKeys deletes existing keys and generates a new pair.
// The secret key is returned once and can't be retrieved again; the public
// key stays retrievable (HandleListAPIKeys).
func HandleRegenerateAPIKeys(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		pubKey, secKey, pubHash, secHash, err := GenerateAPIKeyPair()
		if err != nil {
			slog.Error("generate api keys failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		pubPrefix := pubKey[:14]
		secPrefix := secKey[:14]

		tx, err := pool.Begin(r.Context())
		if err != nil {
			slog.Error("begin tx failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context()) //nolint:errcheck

		if err := lockProjectKeys(r.Context(), tx, projectID); err != nil {
			slog.Error("lock api keys failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		// Delete existing keys.
		if _, err := tx.Exec(r.Context(), `DELETE FROM api_keys WHERE project_id = $1`, projectID); err != nil {
			slog.Error("delete old api keys failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		if err := StoreAPIKeys(r.Context(), tx, projectID, pubKey, pubHash, pubPrefix, secHash, secPrefix); err != nil {
			slog.Error("store new api keys failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			slog.Error("commit tx failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("api keys regenerated", "project_id", projectID)

		logKeyAudit(r, projectID, audit.ActionAPIKeysRegenerated)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(APIKeyCreatedResponse{
			PublicKey: pubKey,
			SecretKey: secKey,
		})
	}
}

// APISecretKeyRotatedResponse carries the new secret key, shown only here.
type APISecretKeyRotatedResponse struct {
	SecretKey string `json:"secret_key"`
}

// HandleRotateSecretKey replaces only the project's secret key. The public
// key, and every app that uses it, stays as it is. The old secret key stops
// working with the next request (keys aren't cached).
func HandleRotateSecretKey(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		secKey, secHash, err := GenerateSecretKey()
		if err != nil {
			slog.Error("generate secret key failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			slog.Error("begin tx failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context()) //nolint:errcheck

		if err := lockProjectKeys(r.Context(), tx, projectID); err != nil {
			slog.Error("lock api keys failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(r.Context(),
			`DELETE FROM api_keys WHERE project_id = $1 AND type = 'secret'`, projectID); err != nil {
			slog.Error("delete old secret key failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO api_keys (project_id, key_hash, key_prefix, type) VALUES ($1, $2, $3, 'secret')`,
			projectID, secHash, secKey[:14]); err != nil {
			slog.Error("store new secret key failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			slog.Error("commit tx failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("secret api key rotated", "project_id", projectID)
		logKeyAudit(r, projectID, audit.ActionAPISecretKeyRotated)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(APISecretKeyRotatedResponse{SecretKey: secKey})
	}
}

// logKeyAudit records a key change in the project's audit log.
func logKeyAudit(r *http.Request, projectID, action string) {
	auditSvc := audit.FromContext(r.Context())
	if auditSvc == nil {
		return
	}
	claims, _ := auth.ClaimsFromContext(r.Context())
	actorID, actorEmail := "", ""
	if claims != nil {
		actorID = claims.Subject
		actorEmail = claims.Email
	}
	auditSvc.Log(r.Context(), projectID, actorID, actorEmail, action,
		audit.WithTarget("api_keys", projectID),
		audit.WithIP(r.RemoteAddr))
}
