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
}

// APIKeyCreatedResponse includes the full plaintext keys (shown once on creation).
type APIKeyCreatedResponse struct {
	PublicKey  string `json:"public_key"`
	SecretKey  string `json:"secret_key"`
}

// HandleListAPIKeys returns the API keys for a project (prefixes only).
func HandleListAPIKeys(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		rows, err := pool.Query(r.Context(),
			`SELECT id, key_prefix, type, created_at, last_used_at
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
			if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.Type, &k.CreatedAt, &k.LastUsed); err != nil {
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
// The plaintext keys are returned once — they cannot be retrieved again.
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

		// Delete existing keys.
		if _, err := tx.Exec(r.Context(), `DELETE FROM api_keys WHERE project_id = $1`, projectID); err != nil {
			slog.Error("delete old api keys failed", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		if err := StoreAPIKeys(r.Context(), tx, projectID, pubHash, pubPrefix, secHash, secPrefix); err != nil {
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

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			claims, _ := auth.ClaimsFromContext(r.Context())
			actorID, actorEmail := "", ""
			if claims != nil {
				actorID = claims.Subject
				actorEmail = claims.Email
			}
			auditSvc.Log(r.Context(), projectID, actorID, actorEmail,
				audit.ActionAPIKeysRegenerated,
				audit.WithTarget("api_keys", projectID),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(APIKeyCreatedResponse{
			PublicKey: pubKey,
			SecretKey: secKey,
		})
	}
}
