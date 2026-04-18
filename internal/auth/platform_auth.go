package auth

import (
	"context"
	"fmt"
	"log/slog"
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

type PlatformAuthService struct {
	pool              *pgxpool.Pool
	jwtSecret         []byte
	emailService      PlatformEmailer
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

// SignUp creates a new platform user with email + bcrypt-hashed password.
// When AllowPublicSignup is false (default), only emails present in the
// platform_allowlist table can register. Others receive a waitlist error.
func (s *PlatformAuthService) SignUp(ctx context.Context, email, password string) (*PlatformAuthResponse, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, fmt.Errorf("email is required")
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("password must be at least 8 characters")
	}

	if !s.AllowPublicSignup {
		var allowed bool
		_ = s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM platform_allowlist WHERE email = $1)`,
			email,
		).Scan(&allowed)
		if !allowed {
			return nil, &WaitlistError{Email: email}
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	var user PlatformUser
	err = s.pool.QueryRow(ctx,
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

	slog.Info("platform user signed up", "user_id", user.ID, "email", user.Email)

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
