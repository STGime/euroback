package enduser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// PlatformUser is the response type for platform-managed end-users.
// Excludes sensitive fields like password_hash.
type PlatformUser struct {
	ID           string                 `json:"id"`
	Email        *string                `json:"email"`
	Phone        *string                `json:"phone,omitempty"`
	DisplayName  *string                `json:"display_name"`
	Providers    []string               `json:"providers"`
	Metadata     map[string]interface{} `json:"metadata"`
	BannedAt     *time.Time             `json:"banned_at"`
	LastSignInAt *time.Time             `json:"last_sign_in_at"`
	CreatedAt    time.Time              `json:"created_at"`
}

type createUserRequest struct {
	Email    string                 `json:"email"`
	Password string                 `json:"password"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type updateUserRequest struct {
	Email       *string                `json:"email,omitempty"`
	DisplayName *string                `json:"display_name,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

// PlatformUserList is the paginated response for listing users.
type PlatformUserList struct {
	Users []PlatformUser `json:"users"`
	Total int            `json:"total"`
}

// PlatformRoutes returns a chi.Router with end-user management handlers.
func PlatformRoutes(pool *pgxpool.Pool, limiter *ratelimit.RateLimiter) chi.Router {
	r := chi.NewRouter()
	r.Get("/", handleListUsers(pool))
	r.Post("/", handleCreateUser(pool))
	r.Get("/{userId}", handleGetUser(pool))
	r.Patch("/{userId}", handleUpdateUser(pool))
	r.Delete("/{userId}", handleDeleteUser(pool))
	r.Post("/{userId}/suspend", handleSuspendUser(pool))
	r.Delete("/{userId}/suspend", handleUnsuspendUser(pool))
	r.Post("/{userId}/reset-password", handleResetPassword(pool))
	r.Get("/{userId}/export", HandleGDPRExport(pool, limiter))
	return r
}

func scanUser(rows interface{ Scan(dest ...interface{}) error }) (PlatformUser, error) {
	var u PlatformUser
	var metadataJSON []byte
	var providersArr []string
	if err := rows.Scan(&u.ID, &u.Email, &u.Phone, &u.DisplayName, &metadataJSON, &u.BannedAt, &u.LastSignInAt, &u.CreatedAt, &providersArr); err != nil {
		return u, err
	}
	if metadataJSON != nil {
		_ = json.Unmarshal(metadataJSON, &u.Metadata)
	}
	if u.Metadata == nil {
		u.Metadata = map[string]interface{}{}
	}
	u.Providers = providersArr
	if u.Providers == nil {
		// If no identities found, infer from password_hash presence.
		u.Providers = []string{"email"}
	}
	return u, nil
}

func isValidEmail(email string) bool {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return false
	}
	local, domain := parts[0], parts[1]
	return local != "" && domain != "" && strings.Contains(domain, ".")
}

func handleListUsers(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		// Search filter.
		search := strings.TrimSpace(r.URL.Query().Get("search"))

		// Pagination.
		limit := 50
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		qs := quoteIdent(schema)
		var countQ, listQ string
		var args []interface{}

		userCols := fmt.Sprintf(
			`u.id, u.email, u.phone, u.display_name, u.metadata, u.banned_at, u.last_sign_in_at, u.created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = u.id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)

		if search != "" {
			pattern := "%" + search + "%"
			countQ = fmt.Sprintf(`SELECT count(*) FROM %s.users WHERE email ILIKE $1 OR display_name ILIKE $1`, qs)
			listQ = fmt.Sprintf(`SELECT %s FROM %s.users u WHERE u.email ILIKE $1 OR u.display_name ILIKE $1 ORDER BY u.created_at DESC LIMIT $2 OFFSET $3`, userCols, qs)
			args = []interface{}{pattern, limit, offset}
		} else {
			countQ = fmt.Sprintf(`SELECT count(*) FROM %s.users`, qs)
			listQ = fmt.Sprintf(`SELECT %s FROM %s.users u ORDER BY u.created_at DESC LIMIT $1 OFFSET $2`, userCols, qs)
			args = []interface{}{limit, offset}
		}

		var total int
		users := []PlatformUser{}
		if err := edb.RunAsService(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			if search != "" {
				if e := tx.QueryRow(ctx, countQ, args[0]).Scan(&total); e != nil {
					return fmt.Errorf("count users: %w", e)
				}
			} else {
				if e := tx.QueryRow(ctx, countQ).Scan(&total); e != nil {
					return fmt.Errorf("count users: %w", e)
				}
			}
			rows, e := tx.Query(ctx, listQ, args...)
			if e != nil {
				return fmt.Errorf("list users: %w", e)
			}
			defer rows.Close()
			for rows.Next() {
				u, e := scanUser(rows)
				if e != nil {
					return fmt.Errorf("scan user: %w", e)
				}
				users = append(users, u)
			}
			return rows.Err()
		}); err != nil {
			slog.Error("list end-users failed", "error", err, "schema", schema)
			http.Error(w, `{"error":"failed to list users"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PlatformUserList{Users: users, Total: total})
	}
}

func handleGetUser(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")
		qs := quoteIdent(schema)
		userCols := fmt.Sprintf(
			`u.id, u.email, u.phone, u.display_name, u.metadata, u.banned_at, u.last_sign_in_at, u.created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = u.id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)
		q := fmt.Sprintf(`SELECT %s FROM %s.users u WHERE u.id = $1`, userCols, qs)
		var u PlatformUser
		if err := edb.RunAsService(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, userID))
			return e
		}); err != nil {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

func handleCreateUser(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		var req createUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email == "" {
			http.Error(w, `{"error":"email is required"}`, http.StatusBadRequest)
			return
		}
		if !isValidEmail(email) {
			http.Error(w, `{"error":"invalid email address"}`, http.StatusBadRequest)
			return
		}
		if len(req.Password) < 8 {
			http.Error(w, `{"error":"password must be at least 8 characters"}`, http.StatusBadRequest)
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			slog.Error("hash password failed", "error", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}

		metadataJSON, _ := json.Marshal(req.Metadata)
		if req.Metadata == nil {
			metadataJSON = []byte("{}")
		}

		qs := quoteIdent(schema)
		q := fmt.Sprintf(
			`INSERT INTO %s.users (email, password_hash, metadata, email_confirmed_at)
			 VALUES ($1, $2, $3, now())
			 RETURNING id, email, phone, display_name, metadata, banned_at, last_sign_in_at, created_at, ARRAY[]::text[]`,
			qs,
		)
		var u PlatformUser
		err = edb.RunAsService(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, email, string(hash), string(metadataJSON)))
			return e
		})
		if err != nil {
			if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
				http.Error(w, `{"error":"email already exists"}`, http.StatusBadRequest)
				return
			}
			slog.Error("create end-user failed", "error", err, "schema", schema)
			http.Error(w, `{"error":"failed to create user"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("platform created end-user", "schema", schema, "user_id", u.ID, "email", u.Email)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(u)
	}
}

func handleUpdateUser(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")

		var req updateUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		// Build SET clauses dynamically.
		setClauses := []string{"updated_at = now()"}
		args := []interface{}{}
		argIdx := 1

		if req.Email != nil {
			email := strings.ToLower(strings.TrimSpace(*req.Email))
			if email == "" {
				http.Error(w, `{"error":"email cannot be empty"}`, http.StatusBadRequest)
				return
			}
			if !isValidEmail(email) {
				http.Error(w, `{"error":"invalid email address"}`, http.StatusBadRequest)
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("email = $%d", argIdx))
			args = append(args, email)
			argIdx++
		}
		if req.DisplayName != nil {
			setClauses = append(setClauses, fmt.Sprintf("display_name = $%d", argIdx))
			args = append(args, *req.DisplayName)
			argIdx++
		}
		if req.Metadata != nil {
			metaJSON, _ := json.Marshal(req.Metadata)
			setClauses = append(setClauses, fmt.Sprintf("metadata = $%d", argIdx))
			args = append(args, string(metaJSON))
			argIdx++
		}

		if len(args) == 0 {
			http.Error(w, `{"error":"no fields to update"}`, http.StatusBadRequest)
			return
		}

		qs := quoteIdent(schema)
		returnCols := fmt.Sprintf(
			`id, email, phone, display_name, metadata, banned_at, last_sign_in_at, created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)
		args = append(args, userID)
		q := fmt.Sprintf(
			`UPDATE %s.users SET %s WHERE id = $%d RETURNING %s`,
			qs, strings.Join(setClauses, ", "), argIdx, returnCols,
		)

		var u PlatformUser
		err := edb.RunAsService(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, args...))
			return e
		})
		if err != nil {
			if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
				http.Error(w, `{"error":"email already taken"}`, http.StatusBadRequest)
				return
			}
			if strings.Contains(err.Error(), "no rows") {
				http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
				return
			}
			slog.Error("update end-user failed", "error", err, "schema", schema, "user_id", userID)
			http.Error(w, `{"error":"failed to update user"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("platform updated end-user", "schema", schema, "user_id", u.ID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

func handleDeleteUser(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")
		if userID == "" {
			http.Error(w, `{"error":"user ID is required"}`, http.StatusBadRequest)
			return
		}

		q := fmt.Sprintf(`DELETE FROM %s.users WHERE id = $1`, quoteIdent(schema))
		var rowsAffected int64
		err := edb.RunAsService(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, q, userID)
			if e != nil {
				return e
			}
			rowsAffected = tag.RowsAffected()
			return nil
		})
		if err != nil {
			slog.Error("delete end-user failed", "error", err, "schema", schema, "user_id", userID)
			http.Error(w, `{"error":"failed to delete user"}`, http.StatusInternalServerError)
			return
		}
		if rowsAffected == 0 {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}

		slog.Info("platform deleted end-user", "schema", schema, "user_id", userID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleSuspendUser(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")
		qs := quoteIdent(schema)
		returnCols := fmt.Sprintf(
			`id, email, phone, display_name, metadata, banned_at, last_sign_in_at, created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)
		q := fmt.Sprintf(
			`UPDATE %s.users SET banned_at = now(), updated_at = now() WHERE id = $1 AND banned_at IS NULL RETURNING %s`,
			qs, returnCols,
		)
		revokeQ := fmt.Sprintf(`UPDATE %s.refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, quoteIdent(schema))
		var u PlatformUser
		err := edb.RunAsService(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, userID))
			if e != nil {
				return e
			}
			_, _ = tx.Exec(ctx, revokeQ, userID)
			return nil
		})
		if err != nil {
			http.Error(w, `{"error":"user not found or already suspended"}`, http.StatusNotFound)
			return
		}

		slog.Info("platform suspended end-user", "schema", schema, "user_id", userID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

func handleUnsuspendUser(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")
		qs := quoteIdent(schema)
		returnCols := fmt.Sprintf(
			`id, email, phone, display_name, metadata, banned_at, last_sign_in_at, created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)
		q := fmt.Sprintf(
			`UPDATE %s.users SET banned_at = NULL, updated_at = now() WHERE id = $1 AND banned_at IS NOT NULL RETURNING %s`,
			qs, returnCols,
		)
		var u PlatformUser
		err := edb.RunAsService(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, userID))
			return e
		})
		if err != nil {
			http.Error(w, `{"error":"user not found or not suspended"}`, http.StatusNotFound)
			return
		}

		slog.Info("platform unsuspended end-user", "schema", schema, "user_id", userID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

func handleResetPassword(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")

		var req resetPasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if len(req.Password) < 8 {
			http.Error(w, `{"error":"password must be at least 8 characters"}`, http.StatusBadRequest)
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			slog.Error("hash password failed", "error", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}

		q := fmt.Sprintf(
			`UPDATE %s.users SET password_hash = $1, updated_at = now() WHERE id = $2`,
			quoteIdent(schema),
		)
		revokeQ := fmt.Sprintf(`UPDATE %s.refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, quoteIdent(schema))
		var rowsAffected int64
		err = edb.RunAsService(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, q, string(hash), userID)
			if e != nil {
				return e
			}
			rowsAffected = tag.RowsAffected()
			if rowsAffected == 0 {
				return nil
			}
			_, _ = tx.Exec(ctx, revokeQ, userID)
			return nil
		})
		if err != nil {
			slog.Error("reset password failed", "error", err, "schema", schema, "user_id", userID)
			http.Error(w, `{"error":"failed to reset password"}`, http.StatusInternalServerError)
			return
		}
		if rowsAffected == 0 {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}

		// Refresh tokens already revoked inside the transaction above.


		slog.Info("platform reset end-user password", "schema", schema, "user_id", userID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "password_reset"})
	}
}
