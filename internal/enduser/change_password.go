package enduser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// ChangePasswordRequest is the body of POST /v1/auth/user/password (#808).
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

var (
	// ErrWrongCurrentPassword: the current password didn't match. Counts
	// as a failed sign-in for the account's brute-force limit.
	ErrWrongCurrentPassword = errors.New("current password is incorrect")
	// ErrNoPassword: the user signs in without a password (magic link,
	// OAuth, phone, passkey) — they set one through a password reset.
	ErrNoPassword = errors.New("this account has no password: set one with a password reset")
	// errPasswordChanged: the password changed between our read and write.
	errPasswordChanged = errors.New("the password was changed at the same time: try again")
)

// ChangePassword lets a signed-in user replace their password, given the
// current one (#808). Every refresh token of the user is revoked — other
// devices are signed out when their access token expires — and the
// caller gets a fresh session so it stays signed in.
func (s *AuthService) ChangePassword(ctx context.Context, schemaName, jwtSecret, projectID string, config tenant.AuthConfig, userID string, req ChangePasswordRequest) (*AuthResponse, error) {
	if !config.IsEmailPasswordEnabled() {
		return nil, fmt.Errorf("email/password authentication is disabled")
	}
	minLen := config.PasswordMinLength
	if minLen < 8 {
		minLen = 8
	}
	switch {
	case len(req.NewPassword) < minLen:
		return nil, fmt.Errorf("password must be at least %d characters", minLen)
	case len(req.NewPassword) > 72:
		return nil, fmt.Errorf("password must be at most 72 bytes")
	}

	qs := quoteIdent(schemaName)
	var user User
	var metadataJSON []byte
	var oldHash *string
	var bannedAt *time.Time
	err := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, email, display_name, avatar_url, metadata, email_confirmed_at, created_at, updated_at, password_hash, banned_at
			   FROM `+qs+`.users WHERE id = $1`, userID).
			Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &metadataJSON, &user.EmailConfirmedAt, &user.CreatedAt, &user.UpdatedAt, &oldHash, &bannedAt)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("query user: %w", err)
	}
	_ = json.Unmarshal(metadataJSON, &user.Metadata)
	if bannedAt != nil {
		return nil, fmt.Errorf("account suspended")
	}
	if oldHash == nil {
		return nil, ErrNoPassword
	}
	if cost, err := bcrypt.Cost([]byte(*oldHash)); err != nil || cost > maxPasswordHashCost {
		slog.Warn("end-user password hash not usable", "schema", schemaName, "user_id", userID)
		return nil, ErrWrongCurrentPassword
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*oldHash), []byte(req.CurrentPassword)); err != nil {
		return nil, ErrWrongCurrentPassword
	}
	// No new session for a user who couldn't sign in now (#806).
	if emailConfirmationPending(config.RequireEmailConfirmation, true, user.EmailConfirmedAt) {
		return nil, ErrEmailNotConfirmed
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	// Write only if the hash is still the one we checked against, and
	// revoke every refresh token in the same transaction.
	err = s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE `+qs+`.users SET password_hash = $1, updated_at = now() WHERE id = $2 AND password_hash = $3`,
			string(newHash), userID, *oldHash)
		if err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return errPasswordChanged
		}
		if _, err := tx.Exec(ctx,
			`UPDATE `+qs+`.refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
			return fmt.Errorf("revoke refresh tokens: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.Info("end-user changed password", "schema", schemaName, "user_id", userID)

	accessToken, expiresIn, err := generateAccessToken(user.ID, user.EmailString(), projectID, jwtSecret, config.SessionDurationSeconds())
	if err != nil {
		return nil, err
	}
	refreshToken, err := s.createRefreshToken(ctx, schemaName, user.ID)
	if err != nil {
		return nil, err
	}
	return &AuthResponse{AccessToken: accessToken, TokenType: "bearer", ExpiresIn: expiresIn, RefreshToken: refreshToken, User: user}, nil
}

// HandleChangePassword serves POST /v1/auth/user/password (end-user JWT).
func HandleChangePassword(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}
		claims, ok := auth.EndUserClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			return
		}

		var req ChangePasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}
		if req.CurrentPassword == "" || req.NewPassword == "" {
			writeJSON(w, map[string]string{"error": "current_password and new_password are required"}, http.StatusBadRequest)
			return
		}

		// Same per-account brute-force counter as sign-in: a stolen
		// session mustn't be a way to guess the password faster.
		email := strings.ToLower(strings.TrimSpace(claims.Email))
		if email != "" && ratelimit.CheckSigninFailRate(rl, w, r.Context(), email) {
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)
		resp, err := svc.ChangePassword(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, claims.UserID, req)
		switch {
		case err == nil:
			writeJSON(w, resp, http.StatusOK)
		case errors.Is(err, ErrWrongCurrentPassword):
			if email != "" {
				ratelimit.RecordSigninFailure(rl, r.Context(), email)
			}
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
		case errors.Is(err, ErrEmailNotConfirmed):
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusForbidden)
		case err.Error() == "account suspended":
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusForbidden)
		case err.Error() == "user not found":
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusNotFound)
		case errors.Is(err, errPasswordChanged):
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusConflict)
		case errors.Is(err, ErrNoPassword),
			strings.HasPrefix(err.Error(), "password must be"),
			err.Error() == "email/password authentication is disabled":
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
		default:
			slog.Error("change password failed", "error", err, "project_id", pc.ProjectID, "user_id", claims.UserID)
			writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
		}
	}
}
