package enduser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/compliance"
	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/events"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	// EmailConfirmedAt is null until the user confirms their email (#806).
	EmailConfirmedAt *time.Time `json:"email_confirmed_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

type createUserRequest struct {
	Email string `json:"email"`
	// Password is optional (#807): without one the user signs in by magic
	// link, OAuth or a password reset.
	Password    string                 `json:"password,omitempty"`
	DisplayName *string                `json:"display_name,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	// EmailConfirmed defaults to true: the developer vouches for the address.
	EmailConfirmed *bool `json:"email_confirmed,omitempty"`
}

// adminPasswordError checks a password an admin sets (create, reset):
// 8 characters minimum, and at most bcrypt's 72 bytes — longer ones made
// GenerateFromPassword fail with a 500.
func adminPasswordError(pw string) string {
	switch {
	case len(pw) < 8:
		return "password must be at least 8 characters"
	case len(pw) > 72:
		return "password must be at most 72 bytes"
	}
	return ""
}

type updateUserRequest struct {
	Email       *string                `json:"email,omitempty"`
	DisplayName *string                `json:"display_name,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	// EmailConfirmed marks the email confirmed (true; keeps an existing
	// date) or unconfirmed (false) — #806.
	EmailConfirmed *bool `json:"email_confirmed,omitempty"`
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

// PlatformUserList is the paginated response for listing users.
type PlatformUserList struct {
	Users []PlatformUser `json:"users"`
	Total int            `json:"total"`
}

// PoolResolver picks the pool that owns the caller's tenant schema:
// the project's dedicated managed-PG instance for Team-tier (post-
// PR-A the tenant schema does not exist on shared for Team-tier), nil
// for Free/Pro (caller falls back to the shared platform pool). Same
// shape as plans.PoolResolver / query.PoolResolver so router.go can
// hand every package the same closure over its PoolCache.
type PoolResolver func(ctx context.Context, projectID string) *pgxpool.Pool

// PlatformRoutes returns a chi.Router with end-user management handlers.
// The optional resolver routes tenant-schema reads / writes to the
// project's dedicated instance for Team-tier; pass nil for Free/Pro-only
// deployments or tests. catalog answers catalog reads (the developer pool
// on the shared cluster); nil uses pool.
func PlatformRoutes(pool *pgxpool.Pool, resolver PoolResolver, limiter *ratelimit.RateLimiter, catalog *pgxpool.Pool) chi.Router {
	r := chi.NewRouter()
	r.Get("/", handleListUsers(pool, resolver))
	r.Post("/", handleCreateUser(pool, resolver, limiter))
	r.Post("/batch", handleBatchCreateUsers(pool, resolver))
	r.Route("/{userId}", func(r chi.Router) {
		r.Use(requireUserID)
		r.Get("/", handleGetUser(pool, resolver))
		r.Patch("/", handleUpdateUser(pool, resolver))
		r.Delete("/", handleDeleteUser(pool, resolver))
		r.Post("/suspend", handleSuspendUser(pool, resolver))
		r.Delete("/suspend", handleUnsuspendUser(pool, resolver))
		r.Post("/reset-password", handleResetPassword(pool, resolver, limiter))
		r.Post("/signout", handleSignOutUser(pool, resolver))
		r.Get("/export", HandleGDPRExport(pool, catalog, limiter))
	})
	return r
}

// Request-size limits for the user write endpoints (console and SDK admin
// API): the body, a display name, and the encoded metadata object.
const (
	maxUserBodyBytes     = 64 << 10
	maxDisplayNameRunes  = 256
	maxUserMetadataBytes = 16 << 10
)

// decodeUserBody reads a JSON body of at most maxUserBodyBytes into dst,
// answering 413 (too large) or 400 itself. Returns false when it answered.
func decodeUserBody(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxUserBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, map[string]string{"error": "request body too large (max 64 KiB)", "code": "body_too_large"}, http.StatusRequestEntityTooLarge)
			return false
		}
		writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
		return false
	}
	return true
}

// userFieldsError checks the size of a display name and of the metadata
// object a create / update sets.
func userFieldsError(displayName *string, metadata map[string]interface{}) string {
	if displayName != nil && utf8.RuneCountInString(*displayName) > maxDisplayNameRunes {
		return "display_name must be at most 256 characters"
	}
	if metadata != nil {
		b, err := json.Marshal(metadata)
		if err != nil {
			return "metadata must be a JSON object"
		}
		if len(b) > maxUserMetadataBytes {
			return "metadata must be at most 16 KiB as JSON"
		}
	}
	return ""
}

// Password hashing (bcrypt, cost 12) is the expensive part of creating a
// user with a password and of setting one. Per project, at most
// hashRateLimit of these calls per hashRateWindow (an average of two a
// second, bursts of ten); beyond that 429 with Retry-After. Shared by the
// console and the SDK admin API. Without a limiter (no Redis) it allows.
const (
	hashRateLimit  = 10
	hashRateWindow = 5 * time.Second
)

// checkHashRate answers 429 and returns true when the project is over its
// password-hashing budget.
func checkHashRate(rl *ratelimit.RateLimiter, w http.ResponseWriter, r *http.Request) bool {
	projectID := query.ProjectIDFromContext(r.Context())
	if rl == nil || projectID == "" {
		return false
	}
	return ratelimit.CheckAuthRateForProject(rl, w, r.Context(), "admin_password_hash", projectID, "project", hashRateLimit, hashRateWindow)
}

func optionalLimiter(limiter []*ratelimit.RateLimiter) *ratelimit.RateLimiter {
	if len(limiter) > 0 {
		return limiter[0]
	}
	return nil
}

// escapeILIKE escapes ILIKE's wildcards (and its escape character) so a
// search matches the text literally.
func escapeILIKE(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// requireUserID answers 404 for a {userId} that isn't a UUID, so a typo
// reads as "user not found" rather than a database error (500).
func requireUserID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := uuid.Parse(chi.URLParam(r, "userId")); err != nil {
			writeJSON(w, map[string]string{"error": "user not found"}, http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isUniqueViolation reports a unique-constraint error (an email already
// taken by another user).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// writeEmailTaken is the answer for an email another user already has.
func writeEmailTaken(w http.ResponseWriter) {
	writeJSON(w, map[string]string{"error": "a user with this email already exists", "code": "email_exists"}, http.StatusConflict)
}

// revokeRefreshTokens revokes every live refresh token of the user, so no
// new access token can be minted for them. Access tokens already issued
// stay valid until they expire. Run inside RunAsAuthService (the
// refresh_tokens policy admits only the auth path).
func revokeRefreshTokens(ctx context.Context, tx pgx.Tx, schema, userID string) (int64, error) {
	tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s.refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, quoteIdent(schema)), userID)
	if err != nil {
		return 0, fmt.Errorf("revoke refresh tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// auditUserAction records an admin change to an end user in the
// project's audit log: the console session's account, or — on the SDK
// admin API — the project's secret key (no actor id; via=secret_key).
// Never pass secrets (passwords) in meta.
func auditUserAction(r *http.Request, action, userID string, meta map[string]interface{}) {
	ctx := r.Context()
	svc := audit.FromContext(ctx)
	if svc == nil {
		return
	}
	if meta == nil {
		meta = map[string]interface{}{}
	}
	actorID, actorEmail := audit.ActorFromContext(ctx)
	if actorID == "" {
		if query.KeyTypeFromContext(ctx) == "secret" {
			actorEmail = "secret API key"
			meta["via"] = "secret_key"
		} else if actorEmail == "" {
			return // no identifiable actor: not an admin request
		}
	}
	ip := audit.ClientIPFromContext(ctx)
	if ip == "" {
		ip = r.RemoteAddr
	}
	// The change is done: record it even if the client has gone away.
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	svc.Log(logCtx, query.ProjectIDFromContext(ctx), actorID, actorEmail, action,
		audit.WithTarget("end_user", userID),
		audit.WithMetadata(meta),
		audit.WithIP(ip),
	)
}

// tenantPool returns the dedicated pool for the caller's project when
// the resolver is wired and the project has one, else the shared
// platform pool. Resolver + projectID are read from the request
// context (set by PlatformTenantContext); handler code doesn't need
// to know the caller's project ID.
func tenantPool(ctx context.Context, sharedPool *pgxpool.Pool, resolver PoolResolver) *pgxpool.Pool {
	if resolver == nil {
		return sharedPool
	}
	projectID := query.ProjectIDFromContext(ctx)
	if projectID == "" {
		return sharedPool
	}
	if p := resolver(ctx, projectID); p != nil {
		return p
	}
	return sharedPool
}

func scanUser(rows interface {
	Scan(dest ...interface{}) error
}) (PlatformUser, error) {
	var u PlatformUser
	var metadataJSON []byte
	var providersArr []string
	if err := rows.Scan(&u.ID, &u.Email, &u.Phone, &u.DisplayName, &metadataJSON, &u.BannedAt, &u.LastSignInAt, &u.EmailConfirmedAt, &u.CreatedAt, &providersArr); err != nil {
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

// isValidEmail is a shape check, not RFC 5322: one @, a non-empty local
// part, a dotted domain, at most 254 bytes, and no whitespace or control
// characters (the address ends up in email headers).
func isValidEmail(email string) bool {
	if len(email) > 254 || strings.Count(email, "@") != 1 {
		return false
	}
	for _, r := range email {
		if r <= ' ' || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	local, domain, _ := strings.Cut(email, "@")
	return local != "" && domain != "" && strings.Contains(domain, ".") &&
		!strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

func handleListUsers(pool *pgxpool.Pool, resolver PoolResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		// Search filter.
		search := strings.TrimSpace(r.URL.Query().Get("search"))
		// email= is an exact, case-insensitive match (stored lower-case),
		// unlike search's substring match (#807).
		exactEmail := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))

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
		// page / per_page (1-based): the SDK admin API's names. They take
		// precedence over limit / offset when given.
		if v := r.URL.Query().Get("per_page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 1_000_000 {
				offset = (n - 1) * limit
			}
		}

		qs := quoteIdent(schema)
		var countQ, listQ string
		var args []interface{}

		userCols := fmt.Sprintf(
			`u.id, u.email, u.phone, u.display_name, u.metadata, u.banned_at, u.last_sign_in_at, u.email_confirmed_at, u.created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = u.id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)

		if exactEmail != "" {
			countQ = fmt.Sprintf(`SELECT count(*) FROM %s.users WHERE email = $1`, qs)
			listQ = fmt.Sprintf(`SELECT %s FROM %s.users u WHERE u.email = $1 ORDER BY u.created_at DESC LIMIT $2 OFFSET $3`, userCols, qs)
			args = []interface{}{exactEmail, limit, offset}
		} else if search != "" {
			// Match email / display_name / id. The id::text cast lets
			// operators paste a UUID from an audit row or a support
			// ticket and find the user without needing to type the
			// email exactly. Phone is intentionally not searched —
			// formatting (E.164 vs national vs spaces) makes ILIKE
			// flaky and the picker UI already covers phone-only
			// users via display_name when one is set.
			pattern := "%" + escapeILIKE(search) + "%"
			countQ = fmt.Sprintf(`SELECT count(*) FROM %s.users WHERE email ILIKE $1 OR display_name ILIKE $1 OR id::text ILIKE $1`, qs)
			listQ = fmt.Sprintf(`SELECT %s FROM %s.users u WHERE u.email ILIKE $1 OR u.display_name ILIKE $1 OR u.id::text ILIKE $1 ORDER BY u.created_at DESC LIMIT $2 OFFSET $3`, userCols, qs)
			args = []interface{}{pattern, limit, offset}
		} else {
			countQ = fmt.Sprintf(`SELECT count(*) FROM %s.users`, qs)
			listQ = fmt.Sprintf(`SELECT %s FROM %s.users u ORDER BY u.created_at DESC LIMIT $1 OFFSET $2`, userCols, qs)
			args = []interface{}{limit, offset}
		}

		var total int
		users := []PlatformUser{}
		if err := edb.RunAsService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			if len(args) == 3 { // filtered: $1 is the filter
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

func handleGetUser(pool *pgxpool.Pool, resolver PoolResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")
		qs := quoteIdent(schema)
		userCols := fmt.Sprintf(
			`u.id, u.email, u.phone, u.display_name, u.metadata, u.banned_at, u.last_sign_in_at, u.email_confirmed_at, u.created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = u.id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)
		q := fmt.Sprintf(`SELECT %s FROM %s.users u WHERE u.id = $1`, userCols, qs)
		var u PlatformUser
		if err := edb.RunAsService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
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

func handleCreateUser(pool *pgxpool.Pool, resolver PoolResolver, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	rl := optionalLimiter(limiter)
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		var req createUserRequest
		if !decodeUserBody(w, r, &req) {
			return
		}
		if msg := userFieldsError(req.DisplayName, req.Metadata); msg != "" {
			writeJSON(w, map[string]string{"error": msg}, http.StatusBadRequest)
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
		var passwordHash *string
		if req.Password != "" {
			if msg := adminPasswordError(req.Password); msg != "" {
				writeJSON(w, map[string]string{"error": msg}, http.StatusBadRequest)
				return
			}
			if checkHashRate(rl, w, r) {
				return
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
			if err != nil {
				slog.Error("hash password failed", "error", err)
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}
			h := string(hash)
			passwordHash = &h
		}
		confirmed := req.EmailConfirmed == nil || *req.EmailConfirmed

		metadataJSON, _ := json.Marshal(req.Metadata)
		if req.Metadata == nil {
			metadataJSON = []byte("{}")
		}

		qs := quoteIdent(schema)
		q := fmt.Sprintf(
			`INSERT INTO %s.users (email, password_hash, display_name, metadata, email_confirmed_at)
			 VALUES ($1, $2, $3, $4, CASE WHEN $5::bool THEN now() END)
			 RETURNING id, email, phone, display_name, metadata, banned_at, last_sign_in_at, email_confirmed_at, created_at, ARRAY[]::text[]`,
			qs,
		)
		var u PlatformUser
		err := edb.RunAsService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, email, passwordHash, req.DisplayName, string(metadataJSON), confirmed))
			return e
		})
		if err != nil {
			if isUniqueViolation(err) {
				writeEmailTaken(w)
				return
			}
			slog.Error("create end-user failed", "error", err, "schema", schema)
			http.Error(w, `{"error":"failed to create user"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("platform created end-user", "schema", schema, "user_id", u.ID)
		auditUserAction(r, audit.ActionEndUserCreated, u.ID, map[string]interface{}{
			"with_password": passwordHash != nil, "email_confirmed": confirmed,
		})
		emitUserEvent(r.Context(), query.ProjectIDFromContext(r.Context()), events.AuthUserCreated,
			User{ID: u.ID, Email: u.Email, Phone: u.Phone, CreatedAt: u.CreatedAt}, "email")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(u)
	}
}

func handleUpdateUser(pool *pgxpool.Pool, resolver PoolResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")

		var req updateUserRequest
		if !decodeUserBody(w, r, &req) {
			return
		}
		if msg := userFieldsError(req.DisplayName, req.Metadata); msg != "" {
			writeJSON(w, map[string]string{"error": msg}, http.StatusBadRequest)
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
		if req.EmailConfirmed != nil {
			if *req.EmailConfirmed {
				setClauses = append(setClauses, "email_confirmed_at = COALESCE(email_confirmed_at, now())")
			} else {
				setClauses = append(setClauses, "email_confirmed_at = NULL")
			}
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

		if len(setClauses) == 1 { // only updated_at
			http.Error(w, `{"error":"no fields to update"}`, http.StatusBadRequest)
			return
		}

		qs := quoteIdent(schema)
		returnCols := fmt.Sprintf(
			`id, email, phone, display_name, metadata, banned_at, last_sign_in_at, email_confirmed_at, created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)
		args = append(args, userID)
		q := fmt.Sprintf(
			`UPDATE %s.users SET %s WHERE id = $%d RETURNING %s`,
			qs, strings.Join(setClauses, ", "), argIdx, returnCols,
		)

		var u PlatformUser
		err := edb.RunAsService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, args...))
			return e
		})
		if err != nil {
			if isUniqueViolation(err) {
				writeEmailTaken(w)
				return
			}
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
				return
			}
			slog.Error("update end-user failed", "error", err, "schema", schema, "user_id", userID)
			http.Error(w, `{"error":"failed to update user"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("platform updated end-user", "schema", schema, "user_id", u.ID)
		fields := []string{}
		if req.Email != nil {
			fields = append(fields, "email")
		}
		if req.EmailConfirmed != nil {
			fields = append(fields, "email_confirmed")
		}
		if req.DisplayName != nil {
			fields = append(fields, "display_name")
		}
		if req.Metadata != nil {
			fields = append(fields, "metadata")
		}
		auditUserAction(r, audit.ActionEndUserUpdated, u.ID, map[string]interface{}{"fields": fields})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

func handleDeleteUser(pool *pgxpool.Pool, resolver PoolResolver) http.HandlerFunc {
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

		// Retention-hold check (M2b, hard blocker #3). Legal-Team
		// tenants can pin specific rows under §50 BRAO / §257 HGB /
		// §147 AO retention. Refuse the delete with 409 + hold
		// metadata so the console can surface "retained until <date>
		// under <legal_basis>" to the operator. Non-Legal-Team
		// projects never see this branch — the retention_holds table
		// is empty for them.
		if projectID := query.ProjectIDFromContext(r.Context()); projectID != "" {
			hs := compliance.NewHoldService(pool)
			targetRef := map[string]any{
				"schema": schema,
				"table":  "users",
				"pkey":   map[string]any{"id": userID},
			}
			hold, herr := hs.IsHeld(r.Context(), projectID, compliance.TargetRow, targetRef)
			if herr != nil {
				// Can't tell whether a retention hold applies: refuse
				// rather than delete a user who may be held.
				slog.Error("retention hold lookup failed", "error", herr, "project_id", projectID, "user_id", userID)
				writeJSON(w, map[string]string{"error": "could not check retention holds; try again"}, http.StatusServiceUnavailable)
				return
			}
			if hold != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":       "user is under retention hold — refuse delete",
					"code":        "retention_hold_active",
					"legal_basis": hold.LegalBasis,
					"expires_at":  hold.ExpiresAt.Format(time.RFC3339),
					"hold_id":     hold.ID,
				})
				return
			}
		}

		q := fmt.Sprintf(`DELETE FROM %s.users WHERE id = $1 RETURNING email, phone`, quoteIdent(schema))
		var deleted User
		err := edb.RunAsService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, q, userID).Scan(&deleted.Email, &deleted.Phone)
		})
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			// A row still references the user through a foreign key
			// without ON DELETE CASCADE / SET NULL — e.g. a file they
			// uploaded (storage_objects.uploaded_by), or one of the
			// project's own tables.
			writeJSON(w, map[string]string{
				"error": "other rows still reference this user (" + pgErr.TableName + "): delete them or reassign them first",
				"code":  "user_referenced",
			}, http.StatusConflict)
			return
		}
		if err != nil {
			slog.Error("delete end-user failed", "error", err, "schema", schema, "user_id", userID)
			http.Error(w, `{"error":"failed to delete user"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("platform deleted end-user", "schema", schema, "user_id", userID)
		auditUserAction(r, audit.ActionEndUserDeleted, userID, nil)
		deleted.ID = userID
		emitUserEvent(r.Context(), query.ProjectIDFromContext(r.Context()), events.AuthUserDeleted, deleted, "")
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleSuspendUser(pool *pgxpool.Pool, resolver PoolResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")
		qs := quoteIdent(schema)
		returnCols := fmt.Sprintf(
			`id, email, phone, display_name, metadata, banned_at, last_sign_in_at, email_confirmed_at, created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)
		q := fmt.Sprintf(
			`UPDATE %s.users SET banned_at = now(), updated_at = now() WHERE id = $1 AND banned_at IS NULL RETURNING %s`,
			qs, returnCols,
		)
		var u PlatformUser
		var revoked int64
		err := edb.RunAsAuthService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, userID))
			if e != nil {
				return e
			}
			revoked, e = revokeRefreshTokens(ctx, tx, schema, userID)
			return e
		})
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, `{"error":"user not found or already suspended"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			slog.Error("suspend end-user failed", "error", err, "schema", schema, "user_id", userID)
			http.Error(w, `{"error":"failed to suspend user"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("platform suspended end-user", "schema", schema, "user_id", userID)
		auditUserAction(r, audit.ActionEndUserSuspended, userID, map[string]interface{}{"refresh_tokens_revoked": revoked})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

func handleUnsuspendUser(pool *pgxpool.Pool, resolver PoolResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")
		qs := quoteIdent(schema)
		returnCols := fmt.Sprintf(
			`id, email, phone, display_name, metadata, banned_at, last_sign_in_at, email_confirmed_at, created_at,
			 COALESCE(ARRAY(SELECT DISTINCT i.provider FROM %s.user_identities i WHERE i.user_id = id ORDER BY i.provider), ARRAY[]::text[])`,
			qs,
		)
		q := fmt.Sprintf(
			`UPDATE %s.users SET banned_at = NULL, updated_at = now() WHERE id = $1 AND banned_at IS NOT NULL RETURNING %s`,
			qs, returnCols,
		)
		var u PlatformUser
		err := edb.RunAsService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			var e error
			u, e = scanUser(tx.QueryRow(ctx, q, userID))
			return e
		})
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, `{"error":"user not found or not suspended"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			slog.Error("unsuspend end-user failed", "error", err, "schema", schema, "user_id", userID)
			http.Error(w, `{"error":"failed to unsuspend user"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("platform unsuspended end-user", "schema", schema, "user_id", userID)
		auditUserAction(r, audit.ActionEndUserUnsuspended, userID, nil)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

func handleResetPassword(pool *pgxpool.Pool, resolver PoolResolver, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	rl := optionalLimiter(limiter)
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}

		userID := chi.URLParam(r, "userId")

		var req resetPasswordRequest
		if !decodeUserBody(w, r, &req) {
			return
		}
		if msg := adminPasswordError(req.Password); msg != "" {
			writeJSON(w, map[string]string{"error": msg}, http.StatusBadRequest)
			return
		}
		if checkHashRate(rl, w, r) {
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
		var rowsAffected, revoked int64
		err = edb.RunAsAuthService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, q, string(hash), userID)
			if e != nil {
				return e
			}
			rowsAffected = tag.RowsAffected()
			if rowsAffected == 0 {
				return nil
			}
			revoked, e = revokeRefreshTokens(ctx, tx, schema, userID)
			return e
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
		auditUserAction(r, audit.ActionEndUserPasswordSet, userID, map[string]interface{}{"refresh_tokens_revoked": revoked})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "password_reset"})
	}
}

// handleSignOutUser revokes all of the user's refresh tokens (every
// session, every device). Access tokens already issued stay valid until
// they expire.
func handleSignOutUser(pool *pgxpool.Pool, resolver PoolResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := query.SchemaFromContext(r.Context())
		if schema == "" {
			http.Error(w, `{"error":"tenant context not available"}`, http.StatusBadRequest)
			return
		}
		userID := chi.URLParam(r, "userId")
		existsQ := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s.users WHERE id = $1)`, quoteIdent(schema))
		var exists bool
		var revoked int64
		err := edb.RunAsAuthService(r.Context(), tenantPool(r.Context(), pool, resolver), func(ctx context.Context, tx pgx.Tx) error {
			if e := tx.QueryRow(ctx, existsQ, userID).Scan(&exists); e != nil || !exists {
				return e
			}
			var e error
			revoked, e = revokeRefreshTokens(ctx, tx, schema, userID)
			return e
		})
		if err != nil {
			slog.Error("sign out end-user failed", "error", err, "schema", schema, "user_id", userID)
			http.Error(w, `{"error":"failed to sign out user"}`, http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}

		slog.Info("platform signed out end-user", "schema", schema, "user_id", userID, "revoked", revoked)
		auditUserAction(r, audit.ActionEndUserSignedOut, userID, map[string]interface{}{"refresh_tokens_revoked": revoked})
		writeJSON(w, map[string]interface{}{"status": "signed_out", "refresh_tokens_revoked": revoked}, http.StatusOK)
	}
}
