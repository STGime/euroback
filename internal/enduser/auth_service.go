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

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// AuthService handles end-user auth operations scoped to a tenant schema.
type AuthService struct {
	pool *pgxpool.Pool
}

// NewAuthService creates a new end-user auth service.
func NewAuthService(pool *pgxpool.Pool) *AuthService {
	return &AuthService{pool: pool}
}

// SignUp creates a new end-user in the given tenant schema.
func (s *AuthService) SignUp(ctx context.Context, schemaName, jwtSecret string, projectID string, req SignUpRequest) (*AuthResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		return nil, fmt.Errorf("email is required")
	}
	if parts := strings.SplitN(email, "@", 2); len(parts) != 2 || parts[0] == "" || parts[1] == "" || !strings.Contains(parts[1], ".") {
		return nil, fmt.Errorf("invalid email address")
	}
	if len(req.Password) < 8 {
		return nil, fmt.Errorf("password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	metadataJSON, _ := json.Marshal(req.Metadata)
	if req.Metadata == nil {
		metadataJSON = []byte("{}")
	}

	var user User
	q := fmt.Sprintf(
		`INSERT INTO %s.users (email, password_hash, metadata, email_confirmed_at)
		 VALUES ($1, $2, $3, now())
		 RETURNING id, email, display_name, avatar_url, metadata, created_at, updated_at`,
		quoteIdent(schemaName),
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

	accessToken, expiresIn, err := generateAccessToken(user.ID, user.Email, projectID, jwtSecret)
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
func (s *AuthService) SignIn(ctx context.Context, schemaName, jwtSecret string, projectID string, req SignInRequest) (*AuthResponse, error) {
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

	// Update last_sign_in_at.
	updateQ := fmt.Sprintf(`UPDATE %s.users SET last_sign_in_at = now() WHERE id = $1`, quoteIdent(schemaName))
	_, _ = s.pool.Exec(ctx, updateQ, user.ID)

	slog.Info("end-user signed in", "schema", schemaName, "user_id", user.ID, "email", user.Email)

	accessToken, expiresIn, err := generateAccessToken(user.ID, user.Email, projectID, jwtSecret)
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
func (s *AuthService) RefreshToken(ctx context.Context, schemaName, jwtSecret, projectID, rawRefreshToken string) (*AuthResponse, error) {
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

	accessToken, expiresIn, err := generateAccessToken(user.ID, user.Email, projectID, jwtSecret)
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
func generateAccessToken(userID, email, projectID, secret string) (string, int, error) {
	expiresIn := 3600 // 1 hour
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
