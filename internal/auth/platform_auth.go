package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// PlatformAuthService handles sign-up, sign-in, and JWT generation
// for platform (console) users stored in public.platform_users.
// PlatformEmailer is the interface for sending platform email tokens.
type PlatformEmailer interface {
	SendPlatformPasswordResetEmail(ctx context.Context, userID, userEmail string) error
	VerifyPlatformToken(ctx context.Context, rawToken, tokenType string) (string, error)
}

// DripEnqueuer is an optional hook the gateway wires up in main.go to
// enqueue the onboarding drip series when a signup succeeds. Runs
// inside the same tx as the platform_users insert so a signup that
// rolls back never leaves orphan drip jobs. Typed as a closure rather
// than a River client to keep the auth package free of a hard River
// dep — cmd/gateway/main.go passes a closure that calls
// workers.EnqueueOnboardingSeries.
//
// Phase C of the public-beta launch plan.
type DripEnqueuer func(ctx context.Context, tx pgx.Tx, userID string, signupTime time.Time) error

type PlatformAuthService struct {
	pool              *pgxpool.Pool
	jwtSecret         []byte
	emailService      PlatformEmailer
	dripEnqueuer      DripEnqueuer
	AllowPublicSignup bool // when false, only emails in platform_allowlist can sign up
}

// WaitlistError is returned when a signup attempt is blocked by the allowlist.
type WaitlistError struct {
	Email string
}

func (e *WaitlistError) Error() string {
	return "waitlist"
}

// SetEmailService sets the email service for password reset emails.
func (s *PlatformAuthService) SetEmailService(svc PlatformEmailer) {
	s.emailService = svc
}

// SetDripEnqueuer wires the Phase-C onboarding-drip enqueue hook.
// Optional — if unset, SignUp just skips the enqueue step and a user
// gets no drip mails. Runs inside the signup tx.
func (s *PlatformAuthService) SetDripEnqueuer(fn DripEnqueuer) {
	s.dripEnqueuer = fn
}

// PlatformUser represents a row from public.platform_users.
type PlatformUser struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name,omitempty"`
}

// PlatformProfile is the full profile returned by the account endpoint.
type PlatformProfile struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	DisplayName  *string    `json:"display_name"`
	Plan         string     `json:"plan"`
	IsSuperadmin bool       `json:"is_superadmin"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSignInAt *time.Time `json:"last_sign_in_at"`
}

// PlatformAuthResponse is returned after successful sign-up or sign-in.
type PlatformAuthResponse struct {
	AccessToken string       `json:"access_token"`
	TokenType   string       `json:"token_type"`
	ExpiresIn   int          `json:"expires_in"`
	User        PlatformUser `json:"user"`
}

// NewPlatformAuthService creates a new service for platform auth.
func NewPlatformAuthService(pool *pgxpool.Pool, jwtSecret string) *PlatformAuthService {
	return &PlatformAuthService{
		pool:      pool,
		jwtSecret: []byte(jwtSecret),
	}
}

// AcceptedDocument represents one click-through consent bundled with a
// signup request. Recorded to `legal_acceptances` in the same tx as
// the `platform_users` insert, so no user row ever lives without its
// consent trail (Phase A of the public-beta launch plan; closes the
// gap called out in docs/legal/v1/dpa.md).
type AcceptedDocument struct {
	Type    string `json:"type"`    // 'terms' | 'dpa' | 'privacy' | …
	Version string `json:"version"` // '2.0'
}

// ErrStaleDocumentVersion is returned when a client sends a document
// version that isn't the currently-required one (superseded or unknown).
// Typed sentinel — the HTTP handler recognises it and returns 400 rather
// than 500, and the console can safely re-fetch the legal-documents list
// on receipt. Fixes #279 review high #1.
type ErrStaleDocumentVersion struct {
	DocumentType string
	Version      string
}

func (e *ErrStaleDocumentVersion) Error() string {
	return fmt.Sprintf("unknown or superseded document version: %s v%s — please refresh the page and try again", e.DocumentType, e.Version)
}

// requiredAcceptances is the set of document types signup MUST record.
// Additional documents (privacy, aup) may be present in the client
// payload and get recorded too, but their absence isn't a hard block —
// only Terms + DPA are load-bearing on GDPR Article 7 lawful-basis
// grounds.
var requiredAcceptances = []string{"terms", "dpa"}

// SignUp creates a new platform user with email + bcrypt-hashed password.
// When AllowPublicSignup is false (default), only emails present in the
// platform_allowlist table can register. Others receive a waitlist error.
//
// `accepted` is the click-through list from the signup form. Must
// include entries for all `requiredAcceptances` document types with
// matching `document_type + document_version` rows in `legal_documents`;
// otherwise SignUp fails without touching `platform_users`. On success,
// one row per accepted document lands in `legal_acceptances` inside the
// same transaction as the user insert, so there's never a user without
// its consent trail.
func (s *PlatformAuthService) SignUp(ctx context.Context, email, password string, accepted []AcceptedDocument, clientIP, userAgent string) (*PlatformAuthResponse, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, fmt.Errorf("email is required")
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("password must be at least 8 characters")
	}
	if err := validateRequiredAcceptances(accepted); err != nil {
		return nil, err
	}

	if !s.AllowPublicSignup {
		var allowed bool
		_ = s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM platform_allowlist WHERE email = $1)`,
			email,
		).Scan(&allowed)
		if !allowed {
			if _, err := s.pool.Exec(ctx,
				`INSERT INTO platform_waitlist (email)
				 VALUES ($1)
				 ON CONFLICT (email) DO UPDATE
				    SET last_attempt_at = now(),
				        attempts        = platform_waitlist.attempts + 1`,
				email,
			); err != nil {
				slog.Warn("record waitlist signup", "email", email, "error", err)
			}
			return nil, &WaitlistError{Email: email}
		}
	}

	// Resolve each accepted (type, version) to a legal_documents row
	// BEFORE opening the user-insert tx. If the client sent a version
	// we don't recognise (e.g. an old browser tab from before we
	// rolled v2), a typed ErrStaleDocumentVersion bubbles up so the
	// HTTP layer returns 400 and the console can re-fetch — better
	// than silently accepting a stale acceptance.
	documents, err := s.resolveAcceptedDocuments(ctx, accepted)
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // committed path returns before deferred rollback

	var user PlatformUser
	err = tx.QueryRow(ctx,
		`INSERT INTO platform_users (email, password_hash, email_confirmed_at)
		 VALUES ($1, $2, now())
		 RETURNING id, email, display_name`,
		email, string(hash),
	).Scan(&user.ID, &user.Email, &user.DisplayName)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			return nil, fmt.Errorf("email already registered")
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	// Write one acceptance row per accepted document. `clientIP` and
	// `userAgent` may be empty (e.g. in a test) — the columns are
	// nullable. IP is parsed via net.ParseIP so a malformed XFF value
	// (`1.2.3.4:5678`, `unknown`, a hostname) writes NULL rather than
	// crashing the INET cast + rolling back the whole signup — #279
	// review high #2. `document_type` is lowercased at insert so the
	// `idx_legal_acceptances_user_type` index is queryable
	// case-consistently — #279 review med #4. The `checksum` we
	// resolved above lands in the row so audit later can prove what
	// bytes the user saw (defensible under GDPR Article 7 even if a
	// future deploy mutates `legal_documents.checksum` — #279 med #3).
	var ipParam interface{}
	if parsed := net.ParseIP(clientIP); parsed != nil {
		ipParam = parsed.String()
	}
	var uaParam interface{}
	if userAgent != "" {
		uaParam = userAgent
	}
	for i, doc := range accepted {
		_, err = tx.Exec(ctx,
			`INSERT INTO legal_acceptances (user_id, document_id, document_type, document_version, document_checksum, ip, user_agent)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			user.ID, documents[i].id, strings.ToLower(doc.Type), doc.Version, documents[i].checksum, ipParam, uaParam,
		)
		if err != nil {
			return nil, fmt.Errorf("record acceptance for %s: %w", doc.Type, err)
		}
	}

	// Enqueue the onboarding drip series inside the same tx (Phase C).
	// If enqueue fails, log-warn and continue: signup should not be
	// blocked by a queue insert. `signupTime = now()` matches what the
	// user just saw in the UI.
	if s.dripEnqueuer != nil {
		if err := s.dripEnqueuer(ctx, tx, user.ID, time.Now().UTC()); err != nil {
			slog.Warn("enqueue onboarding drip failed — signup continues without drip", "user_id", user.ID, "error", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit signup: %w", err)
	}

	slog.Info("platform user signed up", "user_id", user.ID, "email", user.Email, "acceptances", len(accepted))

	// New signups are never superadmin; that flag is granted out-of-band.
	token, expiresIn, err := s.generatePlatformJWT(user.ID, user.Email, false)
	if err != nil {
		return nil, err
	}

	return &PlatformAuthResponse{
		AccessToken: token,
		TokenType:   "bearer",
		ExpiresIn:   expiresIn,
		User:        user,
	}, nil
}

// validateRequiredAcceptances ensures every entry in `requiredAcceptances`
// is present in `accepted`. Returns a user-facing error naming the
// missing docs so the console can highlight the checkboxes.
func validateRequiredAcceptances(accepted []AcceptedDocument) error {
	seen := make(map[string]bool, len(accepted))
	for _, doc := range accepted {
		seen[strings.ToLower(doc.Type)] = true
	}
	var missing []string
	for _, req := range requiredAcceptances {
		if !seen[req] {
			missing = append(missing, req)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("you must accept the following to sign up: %s", strings.Join(missing, ", "))
	}
	return nil
}

// resolvedDocument bundles the DB-resolved fields the caller needs to
// insert into legal_acceptances: the FK id, and the checksum the user
// saw at click-through time. Denormalising the checksum into the
// acceptance row means an audit later can prove exactly which bytes
// the user consented to, even if the `legal_documents` row gets its
// checksum column mutated by a follow-up deploy (#279 review med #3).
type resolvedDocument struct {
	id       string
	checksum string
}

// resolveAcceptedDocuments looks each (type, version) up in
// legal_documents. Returns the resolved rows in the same order as
// the input. A missing row returns a typed ErrStaleDocumentVersion
// so the HTTP layer can 400 + the console can re-fetch.
func (s *PlatformAuthService) resolveAcceptedDocuments(ctx context.Context, accepted []AcceptedDocument) ([]resolvedDocument, error) {
	out := make([]resolvedDocument, len(accepted))
	for i, doc := range accepted {
		var r resolvedDocument
		err := s.pool.QueryRow(ctx,
			`SELECT id, checksum FROM legal_documents
			  WHERE document_type = $1
			    AND version = $2
			    AND active = true
			    AND superseded_at IS NULL`,
			strings.ToLower(doc.Type), doc.Version,
		).Scan(&r.id, &r.checksum)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil, &ErrStaleDocumentVersion{DocumentType: doc.Type, Version: doc.Version}
			}
			return nil, fmt.Errorf("resolve document %s v%s: %w", doc.Type, doc.Version, err)
		}
		out[i] = r
	}
	return out, nil
}

// SignIn authenticates a platform user by email + password.
func (s *PlatformAuthService) SignIn(ctx context.Context, email, password string) (*PlatformAuthResponse, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	var user PlatformUser
	var passwordHash string
	var isSuperadmin bool
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, display_name, password_hash, COALESCE(is_superadmin, false)
		 FROM platform_users
		 WHERE email = $1 AND password_hash IS NOT NULL`,
		email,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &passwordHash, &isSuperadmin)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("invalid email or password")
		}
		return nil, fmt.Errorf("query user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("invalid email or password")
	}

	// Update last sign-in timestamp.
	_, _ = s.pool.Exec(ctx,
		`UPDATE platform_users SET last_sign_in_at = now() WHERE id = $1`,
		user.ID,
	)

	slog.Info("platform user signed in", "user_id", user.ID, "email", user.Email, "is_superadmin", isSuperadmin)

	token, expiresIn, err := s.generatePlatformJWT(user.ID, user.Email, isSuperadmin)
	if err != nil {
		return nil, err
	}

	return &PlatformAuthResponse{
		AccessToken: token,
		TokenType:   "bearer",
		ExpiresIn:   expiresIn,
		User:        user,
	}, nil
}

// generatePlatformJWT creates an HS256 JWT for a platform user. The
// isSuperadmin flag is embedded in the token so downstream middleware can
// gate admin routes without a per-request DB hit; it is re-verified from
// platform_users on sensitive actions.
func (s *PlatformAuthService) generatePlatformJWT(userID, email string, isSuperadmin bool) (string, int, error) {
	expiresIn := 24 * 3600 // 24 hours
	now := time.Now()

	claims := jwt.MapClaims{
		"sub":           userID,
		"email":         email,
		"type":          "platform",
		"iss":           "eurobase",
		"iat":           now.Unix(),
		"exp":           now.Add(time.Duration(expiresIn) * time.Second).Unix(),
		"is_superadmin": isSuperadmin,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", 0, fmt.Errorf("sign JWT: %w", err)
	}

	return signed, expiresIn, nil
}

// GetProfile returns the full profile for a platform user.
func (s *PlatformAuthService) GetProfile(ctx context.Context, userID string) (*PlatformProfile, error) {
	var p PlatformProfile
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, display_name, COALESCE(plan, 'free'), COALESCE(is_superadmin, false), created_at, last_sign_in_at
		 FROM platform_users WHERE id = $1`,
		userID,
	).Scan(&p.ID, &p.Email, &p.DisplayName, &p.Plan, &p.IsSuperadmin, &p.CreatedAt, &p.LastSignInAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("query profile: %w", err)
	}
	return &p, nil
}

// UpdateDisplayName sets the display name for a platform user.
func (s *PlatformAuthService) UpdateDisplayName(ctx context.Context, userID, displayName string) error {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return fmt.Errorf("display name is required")
	}
	if len(displayName) > 100 {
		return fmt.Errorf("display name must be at most 100 characters")
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE platform_users SET display_name = $1 WHERE id = $2`,
		displayName, userID,
	)
	if err != nil {
		return fmt.Errorf("update display name: %w", err)
	}
	return nil
}

// ChangePassword verifies the current password and updates to a new one.
func (s *PlatformAuthService) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if len(newPassword) < 8 {
		return fmt.Errorf("new password must be at least 8 characters")
	}

	var passwordHash string
	err := s.pool.QueryRow(ctx,
		`SELECT password_hash FROM platform_users WHERE id = $1`,
		userID,
	).Scan(&passwordHash)
	if err != nil {
		return fmt.Errorf("query user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(currentPassword)); err != nil {
		return fmt.Errorf("current password is incorrect")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	_, err = s.pool.Exec(ctx,
		`UPDATE platform_users SET password_hash = $1 WHERE id = $2`,
		string(hash), userID,
	)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	slog.Info("platform user changed password", "user_id", userID)
	return nil
}

// DeleteAccount removes a platform user after verifying all projects are deleted.
func (s *PlatformAuthService) DeleteAccount(ctx context.Context, userID string) error {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM projects WHERE owner_id = $1::uuid AND status != 'deleting'`,
		userID,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check projects: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("delete all projects before deleting your account")
	}

	_, err = s.pool.Exec(ctx, `DELETE FROM platform_users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}

	slog.Info("platform user deleted account", "user_id", userID)
	return nil
}

// ValidatePlatformJWT parses and validates a platform JWT, returning the claims.
func (s *PlatformAuthService) ValidatePlatformJWT(tokenStr string) (*Claims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims type")
	}

	// Verify this is a platform token.
	tokenType, _ := mapClaims["type"].(string)
	if tokenType != "platform" {
		return nil, fmt.Errorf("not a platform token")
	}

	sub, _ := mapClaims.GetSubject()
	email, _ := mapClaims["email"].(string)
	isSuperadmin, _ := mapClaims["is_superadmin"].(bool)

	if sub == "" {
		return nil, fmt.Errorf("token missing subject")
	}

	return &Claims{
		Subject:      sub,
		Email:        email,
		IsSuperadmin: isSuperadmin,
	}, nil
}

// ForgotPassword initiates a platform password reset.
// Always returns nil to prevent email enumeration.
func (s *PlatformAuthService) ForgotPassword(ctx context.Context, emailAddr string) error {
	if s.emailService == nil {
		slog.Warn("platform forgot-password: email service not configured")
		return nil
	}

	emailAddr = strings.ToLower(strings.TrimSpace(emailAddr))

	var userID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM platform_users WHERE email = $1`,
		emailAddr,
	).Scan(&userID)
	if err != nil {
		return nil // prevent enumeration
	}

	if err := s.emailService.SendPlatformPasswordResetEmail(ctx, userID, emailAddr); err != nil {
		slog.Error("failed to send platform password reset email", "error", err, "user_id", userID)
	}
	return nil
}

// ResetPasswordWithToken resets a platform user's password using a token.
func (s *PlatformAuthService) ResetPasswordWithToken(ctx context.Context, rawToken, newPassword string) error {
	if len(newPassword) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if s.emailService == nil {
		return fmt.Errorf("email service not configured")
	}

	userID, err := s.emailService.VerifyPlatformToken(ctx, rawToken, "password_reset")
	if err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	_, err = s.pool.Exec(ctx,
		`UPDATE platform_users SET password_hash = $1 WHERE id = $2`,
		string(hash), userID,
	)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	slog.Info("platform user reset password via token", "user_id", userID)
	return nil
}
