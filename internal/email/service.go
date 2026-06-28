package email

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	edb "github.com/eurobase/euroback/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// asService runs fn in an auth-service-role tx on s.pool. All
// email_tokens writes/reads go through this — they live in the
// tenant schema and are hit in pre-auth paths where no end_user
// context exists. Closes #164: migration 000055 narrowed the
// email_tokens RLS policy to require `app.intent=internal_auth_path`,
// which RunAsAuthService sets and the generic SQL handler does NOT.
// So a prompt-injected runSQL via MCP cannot reach email_tokens.
func (s *EmailService) asService(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	return edb.RunAsAuthService(ctx, s.pool, fn)
}

// EmailService provides high-level email operations.
type EmailService struct {
	client     *EmailClient
	pool       *pgxpool.Pool
	consoleURL string

	// senderSvc, when set, is consulted on every project-scoped send.
	// If the project has a verified BYO-SMTP sender, the send routes
	// through that custom SMTP and skips the platform TEM + the
	// platform EmailsPerHour quota (#235 Part 1). nil — or
	// ErrNotConfigured on the lookup — both fall back to the platform
	// path unchanged.
	senderSvc *SenderService
}

// NewEmailService creates a new email service.
func NewEmailService(client *EmailClient, pool *pgxpool.Pool, consoleURL string) *EmailService {
	return &EmailService{
		client:     client,
		pool:       pool,
		consoleURL: strings.TrimRight(consoleURL, "/"),
	}
}

// WithSenderService wires the per-project BYO-SMTP service (#235 Part
// 1). The argument may be nil — in that case the EmailService stays on
// the platform-sender-only path, identical to pre-#235 behaviour.
func (s *EmailService) WithSenderService(svc *SenderService) *EmailService {
	s.senderSvc = svc
	return s
}

// sendProjectScoped is the central dispatcher for "send email for
// project X". If the project has a verified custom SMTP sender,
// the send goes through that path; otherwise it falls back to the
// platform TEM client.
//
// Why a single dispatcher: every Send* helper above (Verification,
// PasswordReset, MagicLink) used to call s.client.Send directly. With
// BYO-SMTP we want one place that knows about the project routing
// decision, so the routing rule lives here, not duplicated five
// times. Each helper now passes projectID along with the rendered
// subject + body.
//
// On per-project send failure (e.g. provider auth failed), we
// surface the error rather than silently falling back to the
// platform sender — the failure is what the operator needs to fix,
// not a hidden retry that hides the problem.
func (s *EmailService) sendProjectScoped(ctx context.Context, projectID, to, subject, htmlBody string) error {
	if s.senderSvc != nil && projectID != "" {
		sender, err := s.senderSvc.LoadForSend(ctx, projectID)
		switch {
		case err == nil:
			// Verified custom sender — use it.
			return sendViaCustomSMTP(ctx, sender, to, subject, htmlBody)
		case errors.Is(err, ErrNotConfigured), errors.Is(err, ErrSenderNotVerified):
			// Fall through to platform send.
		default:
			slog.Warn("project email sender lookup failed, falling back to platform",
				"project_id", projectID, "error", err)
		}
	}
	return s.client.Send(ctx, to, subject, htmlBody)
}

// Configured returns whether TEM credentials are set.
func (s *EmailService) Configured() bool {
	return s.client.Configured()
}

// SendBulkBCC sends a single HTML email to every recipient via BCC.
// Recipients do NOT see each other's addresses. Used by superadmin
// broadcast features (e.g. beta-allowlist invitations) where a single
// announcement goes to many people.
//
// Closes #35. Returns a BulkResult so the handler can surface partial
// success ("8/10 sent · 2 failed") to the console. The error is
// non-nil only when nothing went out at all.
func (s *EmailService) SendBulkBCC(ctx context.Context, recipients []string, subject, htmlBody string) (BulkResult, error) {
	return s.client.SendBulk(ctx, recipients, subject, htmlBody)
}

// SendRaw sends a pre-composed HTML email through the underlying TEM client.
// Used by internal background jobs (e.g. usage alerts) that build their own
// subject + body and don't need token generation or template loading.
func (s *EmailService) SendRaw(ctx context.Context, to, subject, htmlBody string) error {
	return s.client.Send(ctx, to, subject, htmlBody)
}

// SendVerificationEmail sends an email verification link to the end-user.
func (s *EmailService) SendVerificationEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail string) error {
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return err
	}

	q := fmt.Sprintf(
		`INSERT INTO %s.email_tokens (user_id, token_hash, token_type, expires_at)
		 VALUES ($1, $2, 'verification', now() + interval '24 hours')`,
		quoteIdent(schemaName),
	)
	if err := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, userID, tokenHash)
		return err
	}); err != nil {
		return fmt.Errorf("store verification token: %w", err)
	}

	customSubject, customHTML, err := s.loadCustomTemplate(ctx, projectID, "verification")
	if err != nil {
		slog.Warn("failed to load custom template, using default", "error", err)
	}

	actionURL := fmt.Sprintf("%s/verify-email?token=%s&project_id=%s", s.consoleURL, rawToken, projectID)
	subject, body, err := RenderTemplate("verification", customSubject, customHTML, TemplateData{
		UserEmail:   userEmail,
		ProjectName: projectName,
		ActionURL:   actionURL,
		ExpiresIn:   "24 hours",
	})
	if err != nil {
		return fmt.Errorf("render verification email: %w", err)
	}

	return s.sendProjectScoped(ctx, projectID, userEmail, subject, body)
}

// SendPasswordResetEmail sends a password reset link to the end-user.
func (s *EmailService) SendPasswordResetEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail string) error {
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return err
	}

	q := fmt.Sprintf(
		`INSERT INTO %s.email_tokens (user_id, token_hash, token_type, expires_at)
		 VALUES ($1, $2, 'password_reset', now() + interval '1 hour')`,
		quoteIdent(schemaName),
	)
	if err := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, userID, tokenHash)
		return err
	}); err != nil {
		return fmt.Errorf("store password reset token: %w", err)
	}

	customSubject, customHTML, err := s.loadCustomTemplate(ctx, projectID, "password_reset")
	if err != nil {
		slog.Warn("failed to load custom template, using default", "error", err)
	}

	actionURL := fmt.Sprintf("%s/reset-password?token=%s&project_id=%s", s.consoleURL, rawToken, projectID)
	subject, body, err := RenderTemplate("password_reset", customSubject, customHTML, TemplateData{
		UserEmail:   userEmail,
		ProjectName: projectName,
		ActionURL:   actionURL,
		ExpiresIn:   "1 hour",
	})
	if err != nil {
		return fmt.Errorf("render password reset email: %w", err)
	}

	return s.sendProjectScoped(ctx, projectID, userEmail, subject, body)
}

// SendMagicLinkEmail sends a magic link sign-in email to the end-user.
func (s *EmailService) SendMagicLinkEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail string) error {
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return err
	}

	q := fmt.Sprintf(
		`INSERT INTO %s.email_tokens (user_id, token_hash, token_type, expires_at)
		 VALUES ($1, $2, 'magic_link', now() + interval '15 minutes')`,
		quoteIdent(schemaName),
	)
	if err := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, userID, tokenHash)
		return err
	}); err != nil {
		return fmt.Errorf("store magic link token: %w", err)
	}

	customSubject, customHTML, err := s.loadCustomTemplate(ctx, projectID, "magic_link")
	if err != nil {
		slog.Warn("failed to load custom template, using default", "error", err)
	}

	actionURL := fmt.Sprintf("%s/magic-link?token=%s&project_id=%s", s.consoleURL, rawToken, projectID)
	subject, body, err := RenderTemplate("magic_link", customSubject, customHTML, TemplateData{
		UserEmail:   userEmail,
		ProjectName: projectName,
		ActionURL:   actionURL,
		ExpiresIn:   "15 minutes",
	})
	if err != nil {
		return fmt.Errorf("render magic link email: %w", err)
	}

	return s.sendProjectScoped(ctx, projectID, userEmail, subject, body)
}

// SendPlatformPasswordResetEmail sends a password reset email for console users.
func (s *EmailService) SendPlatformPasswordResetEmail(ctx context.Context, userID, userEmail string) error {
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO public.platform_email_tokens (user_id, token_hash, token_type, expires_at)
		 VALUES ($1, $2, 'password_reset', now() + interval '1 hour')`,
		userID, tokenHash,
	)
	if err != nil {
		return fmt.Errorf("store platform reset token: %w", err)
	}

	actionURL := fmt.Sprintf("%s/reset-password?token=%s", s.consoleURL, rawToken)
	subject, body, err := RenderTemplate("password_reset", "", "", TemplateData{
		UserEmail:   userEmail,
		ProjectName: "Eurobase Console",
		ActionURL:   actionURL,
		ExpiresIn:   "1 hour",
	})
	if err != nil {
		return fmt.Errorf("render platform reset email: %w", err)
	}

	// Platform-level email (console user, no tenant). Never routes
	// through a project's BYO-SMTP — that sender belongs to a tenant
	// and platform mail should always come from us.
	return s.client.Send(ctx, userEmail, subject, body)
}

// VerifyToken validates a tenant email token and marks it as used.
// Returns the user ID on success.
func (s *EmailService) VerifyToken(ctx context.Context, schemaName, rawToken, tokenType string) (string, error) {
	tokenHash := hashSHA256(rawToken)

	var userID string
	q := fmt.Sprintf(
		`UPDATE %s.email_tokens
		 SET used_at = now()
		 WHERE token_hash = $1 AND token_type = $2
		   AND expires_at > now() AND used_at IS NULL
		 RETURNING user_id`,
		quoteIdent(schemaName),
	)
	err := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, tokenHash, tokenType).Scan(&userID)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("invalid or expired token")
		}
		return "", fmt.Errorf("verify token: %w", err)
	}

	return userID, nil
}

// VerifyPlatformToken validates a platform email token and marks it as used.
func (s *EmailService) VerifyPlatformToken(ctx context.Context, rawToken, tokenType string) (string, error) {
	tokenHash := hashSHA256(rawToken)

	var userID string
	err := s.pool.QueryRow(ctx,
		`UPDATE public.platform_email_tokens
		 SET used_at = now()
		 WHERE token_hash = $1 AND token_type = $2
		   AND expires_at > now() AND used_at IS NULL
		 RETURNING user_id`,
		tokenHash, tokenType,
	).Scan(&userID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("invalid or expired token")
		}
		return "", fmt.Errorf("verify platform token: %w", err)
	}

	return userID, nil
}

func (s *EmailService) loadCustomTemplate(ctx context.Context, projectID, templateType string) (string, string, error) {
	var subject, bodyHTML string
	err := s.pool.QueryRow(ctx,
		`SELECT subject, body_html FROM public.email_templates
		 WHERE project_id = $1 AND template_type = $2`,
		projectID, templateType,
	).Scan(&subject, &bodyHTML)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", "", nil
		}
		return "", "", err
	}
	return subject, bodyHTML, nil
}

func generateToken() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	raw := hex.EncodeToString(b)
	return raw, hashSHA256(raw), nil
}

func hashSHA256(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
