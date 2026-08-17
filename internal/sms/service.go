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

// maxOTPAttempts is the per-token guess budget. After this many wrong
// hashes against the same active token, the token is killed
// (used_at=now()) so the attacker cannot keep grinding. The user re-
// requests a code via /v1/auth/phone/send-otp to get a fresh one.
//
// 5 is generous enough for a legitimate mistyped code but leaves an
// attacker ~5/10^6 ≈ 5e-6 probability of landing a valid guess per
// issued code — 5 orders of magnitude below the pre-fix
// tenant-wide-hash-match risk.
const maxOTPAttempts = 5

// VerifyOTP validates a phone OTP code and marks it as used. Returns
// the user ID on success.
//
// Closes #233. The verify is now bound to the phone number: we look
// up the user, find their most recent active `phone_verification`
// token, and only compare hashes against that single row. Wrong
// hashes bump `attempts`; at maxOTPAttempts the token is killed.
//
// Contrast with the pre-#233 shape, which matched by
// `WHERE token_hash = $1` alone across the whole tenant — a random
// 6-digit guess would sweep every active phone-OTP in the project,
// giving an attacker 10^6 tries to land any valid code for any user.
// The bound-to-phone shape reduces the attack surface from "any
// tenant phone" to "one specific phone, 5 tries per issued code".
//
// Unknown-phone leak avoidance: an attacker probing a phone number
// that isn't registered gets the same "invalid or expired code" error
// as a wrong code for a real phone. The known/unknown branches share
// a return string so timing/response comparisons can't enumerate.
func (s *Service) VerifyOTP(ctx context.Context, schemaName, phone, code string) (string, error) {
	codeHash := hashSHA256(code)

	// One tx: SELECT the active token FOR UPDATE (blocks concurrent
	// verifies from double-counting or double-consuming), then either
	// mark it used (correct hash) or bump attempts (wrong hash). The
	// per-token row-level lock is what makes the attempt counter
	// concurrency-safe — two racing wrong guesses can't both slip
	// through without incrementing.
	var (
		tokenID     string
		storedHash  string
		userID      string
		attempts    int
		verifyErr   error
	)
	err := edb.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		selectQ := fmt.Sprintf(
			`SELECT et.id, et.token_hash, et.user_id, et.attempts
			   FROM %s.email_tokens et
			   JOIN %s.users u ON u.id = et.user_id
			  WHERE u.phone = $1
			    AND et.token_type = 'phone_verification'
			    AND et.expires_at > now()
			    AND et.used_at IS NULL
			    AND et.attempts < $2
			  ORDER BY et.created_at DESC
			  LIMIT 1
			  FOR UPDATE OF et`,
			quoteIdent(schemaName), quoteIdent(schemaName),
		)
		if e := tx.QueryRow(ctx, selectQ, phone, maxOTPAttempts).
			Scan(&tokenID, &storedHash, &userID, &attempts); e != nil {
			if e == pgx.ErrNoRows {
				// No active token for this phone (unknown, exhausted, or expired).
				verifyErr = fmt.Errorf("invalid or expired code")
				return nil
			}
			return fmt.Errorf("lookup phone otp token: %w", e)
		}

		if storedHash == codeHash {
			markQ := fmt.Sprintf(`UPDATE %s.email_tokens SET used_at = now() WHERE id = $1`, quoteIdent(schemaName))
			if _, e := tx.Exec(ctx, markQ, tokenID); e != nil {
				return fmt.Errorf("mark phone otp used: %w", e)
			}
			return nil
		}

		// Wrong hash: bump attempts; kill the token if this bump
		// hits the cap so the next verify sees it as exhausted.
		bumpQ := fmt.Sprintf(
			`UPDATE %s.email_tokens
			    SET attempts = attempts + 1,
			        used_at  = CASE WHEN attempts + 1 >= $2 THEN now() ELSE used_at END
			  WHERE id = $1`,
			quoteIdent(schemaName),
		)
		if _, e := tx.Exec(ctx, bumpQ, tokenID, maxOTPAttempts); e != nil {
			return fmt.Errorf("bump phone otp attempts: %w", e)
		}
		verifyErr = fmt.Errorf("invalid or expired code")
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("verify phone otp: %w", err)
	}
	if verifyErr != nil {
		return "", verifyErr
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
