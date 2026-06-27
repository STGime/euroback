package sms

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	edb "github.com/eurobase/euroback/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service provides high-level SMS OTP operations.
type Service struct {
	client *Client
	pool   *pgxpool.Pool
}

// NewService creates a new SMS service.
func NewService(client *Client, pool *pgxpool.Pool) *Service {
	return &Service{
		client: client,
		pool:   pool,
	}
}

// Configured returns whether SMS credentials are set.
func (s *Service) Configured() bool {
	return s.client.Configured()
}

// SendOTP generates a 6-digit code, stores the hash, and sends it via SMS.
//
// projectID is threaded through (#227) so future per-project metrics
// and quota enforcement on the SMS path can key off it. The current
// quota check lives in the AuthService caller — same call site that
// has the project's rate-limit config — but having projectID here
// means a future refactor can move the check down without another
// signature break.
func (s *Service) SendOTP(ctx context.Context, projectID, schemaName, userID, phone string) error {
	_ = projectID // reserved for per-project metrics / quota in a follow-up
	code, err := generateOTPCode()
	if err != nil {
		return fmt.Errorf("generate otp: %w", err)
	}

	codeHash := hashSHA256(code)

	q := fmt.Sprintf(
		`INSERT INTO %s.email_tokens (user_id, token_hash, token_type, expires_at)
		 VALUES ($1, $2, 'phone_verification', now() + interval '10 minutes')`,
		quoteIdent(schemaName),
	)
	// Closes #164. Phone OTP writes to email_tokens, which is now
	// RLS-gated behind the internal_auth_path intent (migration 000055).
	if err := edb.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, userID, codeHash)
		return err
	}); err != nil {
		return fmt.Errorf("store phone otp: %w", err)
	}

	message := fmt.Sprintf("Your verification code is %s. It expires in 10 minutes.", code)
	if err := s.client.Send(ctx, phone, message); err != nil {
		return fmt.Errorf("send otp sms: %w", err)
	}

	slog.Info("phone otp sent", "phone", phone, "user_id", userID)
	return nil
}

// VerifyOTP validates a phone OTP code and marks it as used.
// Returns the user ID on success.
func (s *Service) VerifyOTP(ctx context.Context, schemaName, code string) (string, error) {
	codeHash := hashSHA256(code)

	var userID string
	q := fmt.Sprintf(
		`UPDATE %s.email_tokens
		 SET used_at = now()
		 WHERE token_hash = $1 AND token_type = 'phone_verification'
		   AND expires_at > now() AND used_at IS NULL
		 RETURNING user_id`,
		quoteIdent(schemaName),
	)
	err := edb.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, codeHash).Scan(&userID)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("invalid or expired code")
		}
		return "", fmt.Errorf("verify phone otp: %w", err)
	}

	return userID, nil
}

// generateOTPCode returns a cryptographically random 6-digit code.
func generateOTPCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashSHA256(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
