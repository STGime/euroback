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
type PlatformAuthService struct {
	pool      *pgxpool.Pool
	jwtSecret []byte
}

// PlatformUser represents a row from public.platform_users.
type PlatformUser struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name,omitempty"`
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
func (s *PlatformAuthService) SignUp(ctx context.Context, email, password string) (*PlatformAuthResponse, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, fmt.Errorf("email is required")
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("password must be at least 8 characters")
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

	token, expiresIn, err := s.generatePlatformJWT(user.ID, user.Email)
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
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, display_name, password_hash
		 FROM platform_users
		 WHERE email = $1 AND password_hash IS NOT NULL`,
		email,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &passwordHash)
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

	slog.Info("platform user signed in", "user_id", user.ID, "email", user.Email)

	token, expiresIn, err := s.generatePlatformJWT(user.ID, user.Email)
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

// generatePlatformJWT creates an HS256 JWT for a platform user.
func (s *PlatformAuthService) generatePlatformJWT(userID, email string) (string, int, error) {
	expiresIn := 24 * 3600 // 24 hours
	now := time.Now()

	claims := jwt.MapClaims{
		"sub":   userID,
		"email": email,
		"type":  "platform",
		"iss":   "eurobase",
		"iat":   now.Unix(),
		"exp":   now.Add(time.Duration(expiresIn) * time.Second).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", 0, fmt.Errorf("sign JWT: %w", err)
	}

	return signed, expiresIn, nil
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

	if sub == "" {
		return nil, fmt.Errorf("token missing subject")
	}

	return &Claims{
		Subject: sub,
		Email:   email,
	}, nil
}
