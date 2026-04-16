package enduser

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/oauth"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// OAuthSecretLookup returns the decrypted client_secret for a given provider
// in a given project schema. Satisfied by tenant.TenantService.GetOAuthClientSecret.
type OAuthSecretLookup func(ctx context.Context, schemaName, providerName string) (string, error)

// AuthService handles end-user auth operations scoped to a tenant schema.
type AuthService struct {
	pool         *pgxpool.Pool
	emailService *email.EmailService
	oauthSecrets OAuthSecretLookup
}

// NewAuthService creates a new end-user auth service.
func NewAuthService(pool *pgxpool.Pool) *AuthService {
	return &AuthService{pool: pool}
}

// SetEmailService sets the email service for sending verification/reset emails.
func (s *AuthService) SetEmailService(svc *email.EmailService) {
	s.emailService = svc
}

// SetOAuthSecretLookup wires in a function that resolves encrypted OAuth
// client_secret values from the vault at sign-in time. If not set, OAuth
// sign-in will fail with a clear error.
func (s *AuthService) SetOAuthSecretLookup(lookup OAuthSecretLookup) {
	s.oauthSecrets = lookup
}

// SignUp creates a new end-user in the given tenant schema.
func (s *AuthService) SignUp(ctx context.Context, schemaName, jwtSecret string, projectID string, config tenant.AuthConfig, req SignUpRequest) (*AuthResponse, error) {
	if !config.IsEmailPasswordEnabled() {
		return nil, fmt.Errorf("email/password authentication is disabled")
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		return nil, fmt.Errorf("email is required")
	}
	if parts := strings.SplitN(email, "@", 2); len(parts) != 2 || parts[0] == "" || parts[1] == "" || !strings.Contains(parts[1], ".") {
		return nil, fmt.Errorf("invalid email address")
	}
	if len(req.Password) < config.PasswordMinLength {
		return nil, fmt.Errorf("password must be at least %d characters", config.PasswordMinLength)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	metadataJSON, _ := json.Marshal(req.Metadata)
	if req.Metadata == nil {
		metadataJSON = []byte("{}")
	}

	// If email confirmation is required, leave email_confirmed_at as NULL.
	emailConfirmedExpr := "now()"
	if config.RequireEmailConfirmation {
		emailConfirmedExpr = "NULL"
	}

	var user User
	q := fmt.Sprintf(
		`INSERT INTO %s.users (email, password_hash, metadata, email_confirmed_at)
		 VALUES ($1, $2, $3, %s)
		 RETURNING id, email, display_name, avatar_url, metadata, created_at, updated_at`,
		quoteIdent(schemaName), emailConfirmedExpr,
	)
	err = s.pool.QueryRow(ctx, q, email, string(hash), string(metadataJSON)).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			return nil, fmt.Errorf("email already registered")
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	_ = json.Unmarshal(metadataJSON, &user.Metadata)

	slog.Info("end-user signed up", "schema", schemaName, "user_id", user.ID, "email", user.Email)

	// If email confirmation is required and an email service is available, send verification email.
	if config.RequireEmailConfirmation && s.emailService != nil {
		if err := s.emailService.SendVerificationEmail(ctx, projectID, "", schemaName, user.ID, user.Email); err != nil {
			slog.Error("failed to send verification email", "error", err, "user_id", user.ID)
		}
	}

	sessionDurationSecs := config.SessionDurationSeconds()
	accessToken, expiresIn, err := generateAccessToken(user.ID, user.Email, projectID, jwtSecret, sessionDurationSecs)
	if err != nil {
		return nil, err
	}

	refreshToken, err := s.createRefreshToken(ctx, schemaName, user.ID)
	if err != nil {
		return nil, err
	}

	return &AuthResponse{
		AccessToken:  accessToken,
		TokenType:    "bearer",
		ExpiresIn:    expiresIn,
		RefreshToken: refreshToken,
		User:         user,
	}, nil
}

// SignIn authenticates an end-user by email + password.
func (s *AuthService) SignIn(ctx context.Context, schemaName, jwtSecret string, projectID string, config tenant.AuthConfig, req SignInRequest) (*AuthResponse, error) {
	if !config.IsEmailPasswordEnabled() {
		return nil, fmt.Errorf("email/password authentication is disabled")
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	var user User
	var passwordHash string
	var metadataJSON []byte

	var bannedAt *time.Time

	q := fmt.Sprintf(
		`SELECT id, email, display_name, avatar_url, metadata, password_hash, banned_at, created_at, updated_at
		 FROM %s.users
		 WHERE email = $1 AND password_hash IS NOT NULL`,
		quoteIdent(schemaName),
	)
	err := s.pool.QueryRow(ctx, q, email).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &passwordHash, &bannedAt, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("invalid email or password")
		}
		return nil, fmt.Errorf("query user: %w", err)
	}
	_ = json.Unmarshal(metadataJSON, &user.Metadata)

	if bannedAt != nil {
		return nil, fmt.Errorf("account suspended")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		return nil, fmt.Errorf("invalid email or password")
	}

	// Sign-in guard: block unverified users when email confirmation is required.
	if config.RequireEmailConfirmation {
		var emailConfirmedAt *time.Time
		confirmQ := fmt.Sprintf(`SELECT email_confirmed_at FROM %s.users WHERE id = $1`, quoteIdent(schemaName))
		_ = s.pool.QueryRow(ctx, confirmQ, user.ID).Scan(&emailConfirmedAt)
		if emailConfirmedAt == nil {
			return nil, fmt.Errorf("email_not_confirmed")
		}
	}

	// Update last_sign_in_at.
	updateQ := fmt.Sprintf(`UPDATE %s.users SET last_sign_in_at = now() WHERE id = $1`, quoteIdent(schemaName))
	_, _ = s.pool.Exec(ctx, updateQ, user.ID)

	slog.Info("end-user signed in", "schema", schemaName, "user_id", user.ID, "email", user.Email)

	sessionDurationSecs := config.SessionDurationSeconds()
	accessToken, expiresIn, err := generateAccessToken(user.ID, user.Email, projectID, jwtSecret, sessionDurationSecs)
	if err != nil {
		return nil, err
	}

	refreshToken, err := s.createRefreshToken(ctx, schemaName, user.ID)
	if err != nil {
		return nil, err
	}

	return &AuthResponse{
		AccessToken:  accessToken,
		TokenType:    "bearer",
		ExpiresIn:    expiresIn,
		RefreshToken: refreshToken,
		User:         user,
	}, nil
}

// RefreshToken rotates a refresh token and issues a new access + refresh token pair.
func (s *AuthService) RefreshToken(ctx context.Context, schemaName, jwtSecret, projectID string, config tenant.AuthConfig, rawRefreshToken string) (*AuthResponse, error) {
	tokenHash := hashSHA256(rawRefreshToken)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Find and revoke the old refresh token.
	var userID string
	q := fmt.Sprintf(
		`UPDATE %s.refresh_tokens
		 SET revoked_at = now()
		 WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		 RETURNING user_id`,
		quoteIdent(schemaName),
	)
	err = tx.QueryRow(ctx, q, tokenHash).Scan(&userID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("invalid or expired refresh token")
		}
		return nil, fmt.Errorf("revoke old token: %w", err)
	}

	// Look up user.
	var user User
	var metadataJSON []byte
	userQ := fmt.Sprintf(
		`SELECT id, email, display_name, avatar_url, metadata, created_at, updated_at
		 FROM %s.users WHERE id = $1`,
		quoteIdent(schemaName),
	)
	err = tx.QueryRow(ctx, userQ, userID).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}
	_ = json.Unmarshal(metadataJSON, &user.Metadata)

	// Create new refresh token.
	newRawToken, err := generateRandomHex(32)
	if err != nil {
		return nil, err
	}
	newTokenHash := hashSHA256(newRawToken)
	insertQ := fmt.Sprintf(
		`INSERT INTO %s.refresh_tokens (user_id, token_hash, expires_at)
		 VALUES ($1, $2, now() + interval '30 days')`,
		quoteIdent(schemaName),
	)
	_, err = tx.Exec(ctx, insertQ, userID, newTokenHash)
	if err != nil {
		return nil, fmt.Errorf("insert new refresh token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	accessToken, expiresIn, err := generateAccessToken(user.ID, user.Email, projectID, jwtSecret, config.SessionDurationSeconds())
	if err != nil {
		return nil, err
	}

	return &AuthResponse{
		AccessToken:  accessToken,
		TokenType:    "bearer",
		ExpiresIn:    expiresIn,
		RefreshToken: newRawToken,
		User:         user,
	}, nil
}

// SignOut revokes the given refresh token.
func (s *AuthService) SignOut(ctx context.Context, schemaName, rawRefreshToken string) error {
	tokenHash := hashSHA256(rawRefreshToken)
	q := fmt.Sprintf(
		`UPDATE %s.refresh_tokens SET revoked_at = now()
		 WHERE token_hash = $1 AND revoked_at IS NULL`,
		quoteIdent(schemaName),
	)
	_, err := s.pool.Exec(ctx, q, tokenHash)
	return err
}

// GetUser retrieves an end-user by ID.
func (s *AuthService) GetUser(ctx context.Context, schemaName, userID string) (*User, error) {
	var user User
	var metadataJSON []byte
	q := fmt.Sprintf(
		`SELECT id, email, display_name, avatar_url, metadata, created_at, updated_at
		 FROM %s.users WHERE id = $1`,
		quoteIdent(schemaName),
	)
	err := s.pool.QueryRow(ctx, q, userID).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("query user: %w", err)
	}
	_ = json.Unmarshal(metadataJSON, &user.Metadata)
	return &user, nil
}

// createRefreshToken generates and stores a refresh token for a user.
func (s *AuthService) createRefreshToken(ctx context.Context, schemaName, userID string) (string, error) {
	rawToken, err := generateRandomHex(32)
	if err != nil {
		return "", err
	}
	tokenHash := hashSHA256(rawToken)

	q := fmt.Sprintf(
		`INSERT INTO %s.refresh_tokens (user_id, token_hash, expires_at)
		 VALUES ($1, $2, now() + interval '30 days')`,
		quoteIdent(schemaName),
	)
	_, err = s.pool.Exec(ctx, q, userID, tokenHash)
	if err != nil {
		return "", fmt.Errorf("insert refresh token: %w", err)
	}

	return rawToken, nil
}

// generateAccessToken creates an HS256 JWT for an end-user.
func generateAccessToken(userID, email, projectID, secret string, sessionDurationSecs int) (string, int, error) {
	expiresIn := sessionDurationSecs
	if expiresIn <= 0 {
		expiresIn = 3600 // fallback to 1 hour
	}
	now := time.Now()

	claims := jwt.MapClaims{
		"sub":        userID,
		"email":      email,
		"project_id": projectID,
		"type":       "enduser",
		"iss":        "eurobase",
		"iat":        now.Unix(),
		"exp":        now.Add(time.Duration(expiresIn) * time.Second).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", 0, fmt.Errorf("sign JWT: %w", err)
	}

	return signed, expiresIn, nil
}

func generateRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func hashSHA256(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

// quoteIdent quotes a SQL identifier to prevent injection.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// ForgotPassword initiates a password reset for an end-user.
// Always returns nil to prevent email enumeration.
func (s *AuthService) ForgotPassword(ctx context.Context, schemaName, projectID, projectName, emailAddr string) error {
	if s.emailService == nil {
		slog.Warn("forgot-password: email service not configured")
		return nil
	}

	emailAddr = strings.ToLower(strings.TrimSpace(emailAddr))

	var userID string
	q := fmt.Sprintf(`SELECT id FROM %s.users WHERE email = $1`, quoteIdent(schemaName))
	err := s.pool.QueryRow(ctx, q, emailAddr).Scan(&userID)
	if err != nil {
		// User not found — return nil to prevent enumeration.
		return nil
	}

	if err := s.emailService.SendPasswordResetEmail(ctx, projectID, projectName, schemaName, userID, emailAddr); err != nil {
		slog.Error("failed to send password reset email", "error", err, "user_id", userID)
	}
	return nil
}

// ResetPassword completes a password reset using a token.
func (s *AuthService) ResetPassword(ctx context.Context, schemaName, rawToken, newPassword string, minPasswordLen int) error {
	if len(newPassword) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	if s.emailService == nil {
		return fmt.Errorf("email service not configured")
	}

	userID, err := s.emailService.VerifyToken(ctx, schemaName, rawToken, "password_reset")
	if err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	// Update password.
	updateQ := fmt.Sprintf(`UPDATE %s.users SET password_hash = $1, updated_at = now() WHERE id = $2`, quoteIdent(schemaName))
	if _, err := s.pool.Exec(ctx, updateQ, string(hash), userID); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	// Revoke all refresh tokens for security.
	revokeQ := fmt.Sprintf(`UPDATE %s.refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, quoteIdent(schemaName))
	if _, err := s.pool.Exec(ctx, revokeQ, userID); err != nil {
		slog.Error("failed to revoke refresh tokens after password reset", "error", err, "user_id", userID)
	}

	slog.Info("end-user password reset", "schema", schemaName, "user_id", userID)
	return nil
}

// VerifyEmail confirms an end-user's email address.
func (s *AuthService) VerifyEmail(ctx context.Context, schemaName, rawToken string) error {
	if s.emailService == nil {
		return fmt.Errorf("email service not configured")
	}

	userID, err := s.emailService.VerifyToken(ctx, schemaName, rawToken, "verification")
	if err != nil {
		return err
	}

	q := fmt.Sprintf(`UPDATE %s.users SET email_confirmed_at = now(), updated_at = now() WHERE id = $1`, quoteIdent(schemaName))
	if _, err := s.pool.Exec(ctx, q, userID); err != nil {
		return fmt.Errorf("confirm email: %w", err)
	}

	slog.Info("end-user email verified", "schema", schemaName, "user_id", userID)
	return nil
}

// RequestMagicLink sends a magic link email for passwordless sign-in.
// Always returns nil to prevent email enumeration.
func (s *AuthService) RequestMagicLink(ctx context.Context, schemaName, projectID, projectName string, config tenant.AuthConfig, emailAddr string) error {
	if !config.IsMagicLinkEnabled() {
		return fmt.Errorf("magic link authentication is disabled")
	}

	if s.emailService == nil {
		slog.Warn("request-magic-link: email service not configured")
		return nil
	}

	emailAddr = strings.ToLower(strings.TrimSpace(emailAddr))

	var userID string
	q := fmt.Sprintf(`SELECT id FROM %s.users WHERE email = $1`, quoteIdent(schemaName))
	err := s.pool.QueryRow(ctx, q, emailAddr).Scan(&userID)
	if err != nil {
		// User not found — return nil to prevent enumeration.
		return nil
	}

	if err := s.emailService.SendMagicLinkEmail(ctx, projectID, projectName, schemaName, userID, emailAddr); err != nil {
		slog.Error("failed to send magic link email", "error", err, "user_id", userID)
	}
	return nil
}

// SignInWithMagicLink completes a magic link sign-in using a token.
func (s *AuthService) SignInWithMagicLink(ctx context.Context, schemaName, jwtSecret, projectID string, config tenant.AuthConfig, rawToken string) (*AuthResponse, error) {
	if !config.IsMagicLinkEnabled() {
		return nil, fmt.Errorf("magic link authentication is disabled")
	}

	if s.emailService == nil {
		return nil, fmt.Errorf("email service not configured")
	}

	userID, err := s.emailService.VerifyToken(ctx, schemaName, rawToken, "magic_link")
	if err != nil {
		return nil, err
	}

	// Fetch the user.
	var user User
	var metadataJSON []byte
	var bannedAt *time.Time
	var emailConfirmedAt *time.Time

	userQ := fmt.Sprintf(
		`SELECT id, email, display_name, avatar_url, metadata, banned_at, email_confirmed_at, created_at, updated_at
		 FROM %s.users WHERE id = $1`,
		quoteIdent(schemaName),
	)
	err = s.pool.QueryRow(ctx, userQ, userID).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &bannedAt, &emailConfirmedAt, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}
	_ = json.Unmarshal(metadataJSON, &user.Metadata)

	if bannedAt != nil {
		return nil, fmt.Errorf("account suspended")
	}

	// Magic link confirms email implicitly.
	if emailConfirmedAt == nil {
		confirmQ := fmt.Sprintf(`UPDATE %s.users SET email_confirmed_at = now(), updated_at = now() WHERE id = $1`, quoteIdent(schemaName))
		_, _ = s.pool.Exec(ctx, confirmQ, userID)
	}

	// Update last_sign_in_at.
	updateQ := fmt.Sprintf(`UPDATE %s.users SET last_sign_in_at = now() WHERE id = $1`, quoteIdent(schemaName))
	_, _ = s.pool.Exec(ctx, updateQ, userID)

	slog.Info("end-user signed in via magic link", "schema", schemaName, "user_id", user.ID, "email", user.Email)

	sessionDurationSecs := config.SessionDurationSeconds()
	accessToken, expiresIn, err := generateAccessToken(user.ID, user.Email, projectID, jwtSecret, sessionDurationSecs)
	if err != nil {
		return nil, err
	}

	refreshToken, err := s.createRefreshToken(ctx, schemaName, user.ID)
	if err != nil {
		return nil, err
	}

	return &AuthResponse{
		AccessToken:  accessToken,
		TokenType:    "bearer",
		ExpiresIn:    expiresIn,
		RefreshToken: refreshToken,
		User:         user,
	}, nil
}

// SignInWithOAuth exchanges an OAuth authorization code for user info,
// finds or creates the user, and returns an auth session.
func (s *AuthService) SignInWithOAuth(ctx context.Context, schemaName, jwtSecret, projectID string, config tenant.AuthConfig, providerName, code, redirectURL string) (*AuthResponse, error) {
	// Get OAuth provider config from auth_config (client_id + enabled flag).
	providerConfig, ok := config.GetOAuthProvider(providerName)
	if !ok {
		return nil, fmt.Errorf("oauth provider %q is not enabled", providerName)
	}

	// Load the encrypted client_secret from the vault.
	if s.oauthSecrets == nil {
		return nil, fmt.Errorf("oauth not available: vault lookup not configured on gateway")
	}
	clientSecret, err := s.oauthSecrets(ctx, schemaName, providerName)
	if err != nil {
		return nil, fmt.Errorf("load oauth secret for %q: %w", providerName, err)
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("oauth provider %q is missing a client_secret (set one in the console)", providerName)
	}

	// Get the provider implementation.
	provider, err := oauth.Get(providerName)
	if err != nil {
		return nil, err
	}

	// Exchange code for user info.
	userInfo, err := provider.ExchangeCode(ctx, oauth.ExchangeConfig{
		ClientID:     providerConfig.ClientID,
		ClientSecret: clientSecret,
		Code:         code,
		RedirectURL:  redirectURL,
		TeamID:       providerConfig.TeamID,
		KeyID:        providerConfig.KeyID,
		TenantID:     providerConfig.TenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("oauth exchange failed: %w", err)
	}

	var user User
	var metadataJSON []byte
	var bannedAt *time.Time

	// Try to find user by provider + provider_user_id.
	findQ := fmt.Sprintf(
		`SELECT id, email, display_name, avatar_url, metadata, banned_at, created_at, updated_at
		 FROM %s.users
		 WHERE provider = $1 AND provider_user_id = $2`,
		quoteIdent(schemaName),
	)
	err = s.pool.QueryRow(ctx, findQ, providerName, userInfo.ProviderID).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &bannedAt, &user.CreatedAt, &user.UpdatedAt)

	if err == pgx.ErrNoRows {
		// Try to find existing user by email (link accounts).
		emailLower := strings.ToLower(strings.TrimSpace(userInfo.Email))
		findByEmailQ := fmt.Sprintf(
			`SELECT id, email, display_name, avatar_url, metadata, banned_at, created_at, updated_at
			 FROM %s.users
			 WHERE email = $1`,
			quoteIdent(schemaName),
		)
		err = s.pool.QueryRow(ctx, findByEmailQ, emailLower).
			Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &bannedAt, &user.CreatedAt, &user.UpdatedAt)

		if err == pgx.ErrNoRows {
			// Create new user.
			var displayName *string
			if userInfo.Name != "" {
				displayName = &userInfo.Name
			}
			var avatarURL *string
			if userInfo.AvatarURL != "" {
				avatarURL = &userInfo.AvatarURL
			}

			insertQ := fmt.Sprintf(
				`INSERT INTO %s.users (email, display_name, avatar_url, provider, provider_user_id, email_confirmed_at, metadata)
				 VALUES ($1, $2, $3, $4, $5, now(), '{}'::jsonb)
				 RETURNING id, email, display_name, avatar_url, metadata, created_at, updated_at`,
				quoteIdent(schemaName),
			)
			err = s.pool.QueryRow(ctx, insertQ, emailLower, displayName, avatarURL, providerName, userInfo.ProviderID).
				Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &user.CreatedAt, &user.UpdatedAt)
			if err != nil {
				return nil, fmt.Errorf("create oauth user: %w", err)
			}
			_ = json.Unmarshal(metadataJSON, &user.Metadata)

			slog.Info("end-user signed up via oauth", "schema", schemaName, "user_id", user.ID, "provider", providerName)
		} else if err != nil {
			return nil, fmt.Errorf("query user by email: %w", err)
		} else {
			// Link existing account: set provider + provider_user_id.
			_ = json.Unmarshal(metadataJSON, &user.Metadata)
			linkQ := fmt.Sprintf(
				`UPDATE %s.users SET provider = $1, provider_user_id = $2, email_confirmed_at = COALESCE(email_confirmed_at, now()), updated_at = now() WHERE id = $3`,
				quoteIdent(schemaName),
			)
			_, _ = s.pool.Exec(ctx, linkQ, providerName, userInfo.ProviderID, user.ID)
			slog.Info("end-user linked oauth account", "schema", schemaName, "user_id", user.ID, "provider", providerName)
		}
	} else if err != nil {
		return nil, fmt.Errorf("query user by provider: %w", err)
	} else {
		_ = json.Unmarshal(metadataJSON, &user.Metadata)
	}

	if bannedAt != nil {
		return nil, fmt.Errorf("account suspended")
	}

	// Update last_sign_in_at.
	updateQ := fmt.Sprintf(`UPDATE %s.users SET last_sign_in_at = now() WHERE id = $1`, quoteIdent(schemaName))
	_, _ = s.pool.Exec(ctx, updateQ, user.ID)

	slog.Info("end-user signed in via oauth", "schema", schemaName, "user_id", user.ID, "provider", providerName)

	sessionDurationSecs := config.SessionDurationSeconds()
	accessToken, expiresIn, err := generateAccessToken(user.ID, user.Email, projectID, jwtSecret, sessionDurationSecs)
	if err != nil {
		return nil, err
	}

	refreshToken, err := s.createRefreshToken(ctx, schemaName, user.ID)
	if err != nil {
		return nil, err
	}

	return &AuthResponse{
		AccessToken:  accessToken,
		TokenType:    "bearer",
		ExpiresIn:    expiresIn,
		RefreshToken: refreshToken,
		User:         user,
	}, nil
}

// ResendVerification resends the verification email to an end-user.
func (s *AuthService) ResendVerification(ctx context.Context, schemaName, projectID, projectName, emailAddr string) error {
	if s.emailService == nil {
		slog.Warn("resend-verification: email service not configured")
		return nil
	}

	emailAddr = strings.ToLower(strings.TrimSpace(emailAddr))

	var userID string
	var emailConfirmedAt *time.Time
	q := fmt.Sprintf(`SELECT id, email_confirmed_at FROM %s.users WHERE email = $1`, quoteIdent(schemaName))
	err := s.pool.QueryRow(ctx, q, emailAddr).Scan(&userID, &emailConfirmedAt)
	if err != nil {
		return nil // prevent enumeration
	}

	if emailConfirmedAt != nil {
		return nil // already confirmed
	}

	if err := s.emailService.SendVerificationEmail(ctx, projectID, projectName, schemaName, userID, emailAddr); err != nil {
		slog.Error("failed to resend verification email", "error", err, "user_id", userID)
	}
	return nil
}
