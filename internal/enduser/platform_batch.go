package enduser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/query"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// maxBatchUsers caps POST /users/batch (#807): one transaction, one
// INSERT per user — a few hundred rows is well inside a request.
const maxBatchUsers = 500

// batchUser is one user to import. Passwords come as bcrypt hashes
// (Supabase exports them in this format) or not at all: hashing hundreds
// of plain passwords at cost 12 would take minutes in one request.
type batchUser struct {
	ID           string                 `json:"id,omitempty"`
	Email        string                 `json:"email"`
	PasswordHash string                 `json:"password_hash,omitempty"`
	Password     string                 `json:"password,omitempty"` // refused: use POST /users
	DisplayName  *string                `json:"display_name,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	// EmailConfirmed defaults to true, as for POST /users.
	EmailConfirmed *bool `json:"email_confirmed,omitempty"`
}

type batchRequest struct {
	Users []batchUser `json:"users"`
}

// batchResult reports one input user, in input order.
type batchResult struct {
	Index  int    `json:"index"`
	Email  string `json:"email"`
	Status string `json:"status"` // created | exists | invalid
	ID     string `json:"id,omitempty"`
	Error  string `json:"error,omitempty"`
}

type batchResponse struct {
	Created int           `json:"created"`
	Exists  int           `json:"exists"`
	Invalid int           `json:"invalid"`
	Results []batchResult `json:"results"`
}

// validateBatchUser normalises u in place and returns why it can't be
// imported ("" = ok).
func validateBatchUser(u *batchUser) string {
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	if u.Email == "" {
		return "email is required"
	}
	if !isValidEmail(u.Email) {
		return "invalid email address"
	}
	if u.Password != "" {
		return "plain passwords aren't accepted in a batch: send password_hash (bcrypt), leave it out, or use POST /users"
	}
	if u.PasswordHash != "" {
		if _, err := bcrypt.Cost([]byte(u.PasswordHash)); err != nil {
			return "password_hash must be a bcrypt hash"
		}
	}
	if u.ID != "" {
		id, err := uuid.Parse(u.ID)
		if err != nil {
			return "id must be a UUID"
		}
		u.ID = id.String()
	}
	return ""
}

// handleBatchCreateUsers imports up to maxBatchUsers users in one
// transaction. Users that are invalid are reported and skipped; users
// whose email (or id) already exists are reported as "exists" and left
// unchanged. A database error rolls the whole batch back.
func handleBatchCreateUsers(pool *pgxpool.Pool, resolver PoolResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		var req batchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if len(req.Users) == 0 {
			http.Error(w, `{"error":"users is empty"}`, http.StatusBadRequest)
			return
		}
		if len(req.Users) > maxBatchUsers {
			writeJSON(w, map[string]string{"error": fmt.Sprintf("at most %d users per request", maxBatchUsers)}, http.StatusBadRequest)
			return
		}

		resp := batchResponse{Results: make([]batchResult, len(req.Users))}
		seenEmail := map[string]int{}
		seenID := map[string]int{}
		for i := range req.Users {
			u := &req.Users[i]
			res := &resp.Results[i]
			res.Index = i
			msg := validateBatchUser(u)
			res.Email = u.Email
			if msg == "" {
				if j, dup := seenEmail[u.Email]; dup {
					msg = fmt.Sprintf("same email as users[%d]", j)
				} else if j, dup := seenID[u.ID]; u.ID != "" && dup {
					msg = fmt.Sprintf("same id as users[%d]", j)
				}
			}
			if msg != "" {
				res.Status, res.Error = "invalid", msg
				resp.Invalid++
				continue
			}
			seenEmail[u.Email] = i
			if u.ID != "" {
				seenID[u.ID] = i
			}
		}

		// ON CONFLICT DO NOTHING (no target) covers every unique index:
		// email, the primary key (a given id), phone. No row back = exists.
		q := fmt.Sprintf(
			`INSERT INTO %s.users (id, email, password_hash, display_name, metadata, email_confirmed_at)
			 VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5, CASE WHEN $6::bool THEN now() END)
			 ON CONFLICT DO NOTHING
			 RETURNING id::text`,
			quoteIdent(schema),
		)
		err := edb.RunAsService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			for i := range req.Users {
				res := &resp.Results[i]
				if res.Status == "invalid" {
					continue
				}
				u := req.Users[i]
				var id, hash *string
				if u.ID != "" {
					id = &u.ID
				}
				if u.PasswordHash != "" {
					hash = &u.PasswordHash
				}
				meta := []byte("{}")
				if u.Metadata != nil {
					meta, _ = json.Marshal(u.Metadata)
				}
				confirmed := u.EmailConfirmed == nil || *u.EmailConfirmed
				var newID string
				switch err := tx.QueryRow(ctx, q, id, u.Email, hash, u.DisplayName, string(meta), confirmed).Scan(&newID); err {
				case nil:
					res.Status, res.ID = "created", newID
				case pgx.ErrNoRows:
					res.Status = "exists"
				default:
					return fmt.Errorf("insert users[%d]: %w", i, err)
				}
			}
			return nil
		})
		if err != nil {
			slog.Error("batch create end-users failed", "error", err, "schema", schema)
			http.Error(w, `{"error":"failed to import users; nothing was created"}`, http.StatusInternalServerError)
			return
		}
		for _, res := range resp.Results {
			switch res.Status {
			case "created":
				resp.Created++
			case "exists":
				resp.Exists++
			}
		}

		slog.Info("platform batch-created end-users", "schema", schema, "created", resp.Created, "exists", resp.Exists, "invalid", resp.Invalid)
		writeJSON(w, resp, http.StatusOK)
	}
}
