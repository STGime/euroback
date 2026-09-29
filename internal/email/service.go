package email

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/plans"
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

	// planGate, when set, keeps custom SMTP to the plans that include it:
	// a project off those plans (never upgraded, or downgraded) sends
	// through the platform even with a verified sender saved.
	planGate PlanGate

	// templateGate, when set, keeps custom email templates to the plans
	// that include them: off-plan (a downgrade) the defaults are sent;
	// the saved templates stay for after an upgrade.
	templateGate TemplateGate

	// deliveryLog, when set, records every auth email's outcome for the
	// project's developers (000135). nil = no log.
	deliveryLog *DeliveryLog
}

// TemplateGate says whether the project's plan includes custom email
// templates (plans.LimitsService.CheckCustomTemplates; Pro and up).
type TemplateGate interface {
	CheckCustomTemplates(ctx context.Context, projectID string) error
}

// WithTemplateGate wires the plan check for custom templates.
func (s *EmailService) WithTemplateGate(g TemplateGate) *EmailService {
	s.templateGate = g
	return s
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

// WithPlanGate wires the plan check for custom SMTP.
func (s *EmailService) WithPlanGate(g PlanGate) *EmailService {
	s.planGate = g
	return s
}

// refuseOffPlanSender runs when a verified custom sender isn't used
// because of the plan. Off-plan, it's un-verified: after an upgrade the
// admin re-tests it rather than auth mail going to credentials that may
// have gone stale meanwhile. A failed plan lookup only logs — the project
// may well be paying, and its sender stays as it is.
func (s *EmailService) refuseOffPlanSender(ctx context.Context, projectID string, err error) {
	if !errors.Is(err, plans.ErrNotOnPlan) {
		slog.Error("custom SMTP: plan check failed, this email goes out through the platform sender",
			"project_id", projectID, "error", err)
		return
	}
	if uerr := s.senderSvc.MarkPlanRefused(ctx, projectID); uerr != nil {
		slog.Error("un-verify off-plan custom SMTP sender failed", "project_id", projectID, "error", uerr)
	}
}

// WithDeliveryLog wires the per-project auth email log.
func (s *EmailService) WithDeliveryLog(l *DeliveryLog) *EmailService {
	s.deliveryLog = l
	return s
}

// RecordSkipped logs an auth email that was deliberately not sent (no
// such user, redirect not allowed, …). The SDK caller still gets "OK";
// the project's developers see the reason in the console.
func (s *EmailService) RecordSkipped(ctx context.Context, projectID, flow, to, reason, detail string) {
	if s == nil {
		return
	}
	s.deliveryLog.Record(ctx, projectID, DeliveryLogEntry{
		Flow: flow, Recipient: to, Outcome: OutcomeSkipped, Reason: reason, Detail: detail,
	})
}

// recordSend logs the outcome of an auth email send.
func (s *EmailService) recordSend(ctx context.Context, projectID, flow, to string, d Delivery, err error) {
	s.deliveryLog.Record(ctx, projectID, classifySend(flow, to, d, err))
}

// classifySend turns a send's result into a log entry.
func classifySend(flow, to string, d Delivery, err error) DeliveryLogEntry {
	e := DeliveryLogEntry{Flow: flow, Recipient: to, Via: d.Via, MessageID: d.MessageID}
	switch {
	case err == nil && d.Via == "":
		e.Outcome, e.Reason = OutcomeSkipped, ReasonEmailNotConfigured
	case err == nil:
		e.Outcome = OutcomeSent
	case d.Via == ViaCustomSMTP:
		e.Outcome, e.Reason, e.Detail = OutcomeFailed, ReasonCustomSMTPFailed, DescribeSMTPError(err)
	case d.Via == ViaPlatform:
		// TEM's error body is operator detail; the project gets the status.
		e.Outcome, e.Reason, e.Detail = OutcomeFailed, ReasonPlatformFailed, "Eurobase's email service didn't accept the message."
		var te *TEMError
		if errors.As(err, &te) {
			e.Detail = fmt.Sprintf("Eurobase's email service didn't accept the message (HTTP %d).", te.Status)
		}
	default:
		e.Outcome, e.Reason, e.Detail = OutcomeFailed, ReasonInternal, "The email couldn't be prepared."
	}
	return e
}

// Delivery is how an email left the platform: Via "" when it didn't get
// as far as a send (not configured, or failed while being prepared).
type Delivery struct {
	Via       string
	MessageID string // TEM email id, platform sends only
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
func (s *EmailService) sendProjectScoped(ctx context.Context, projectID, to, subject, htmlBody string) (Delivery, error) {
	if s.senderSvc != nil && projectID != "" {
		sender, err := s.senderSvc.LoadForSend(ctx, projectID)
		switch {
		case err == nil:
			// Not on this plan (or the plan couldn't be checked): the
			// platform sends — never the custom sender without a plan
			// that includes it.
			if s.planGate != nil {
				if perr := s.planGate.CheckBYOSMTP(ctx, projectID); perr != nil {
					s.refuseOffPlanSender(ctx, projectID, perr)
					break
				}
			}
			// A sender verified before the host / port / encryption / login
			// rules existed may break them. It is never used: it's un-
			// verified with the reason (the console shows it) and the
			// email goes out through the platform, as for an unverified one.
			if rerr := senderUsable(sender); rerr != nil {
				slog.Warn("custom SMTP sender no longer allowed, falling back to platform",
					"project_id", projectID, "reason", rerr)
				if uerr := s.senderSvc.MarkRefused(ctx, projectID, rerr.Error()); uerr != nil {
					slog.Error("un-verify custom SMTP sender failed", "project_id", projectID, "error", uerr)
				}
				break
			}
			// Verified custom sender — use it.
			return Delivery{Via: ViaCustomSMTP}, sendViaCustomSMTP(ctx, sender, to, subject, htmlBody)
		case errors.Is(err, ErrNotConfigured), errors.Is(err, ErrSenderNotVerified):
			// Fall through to platform send.
		default:
			slog.Warn("project email sender lookup failed, falling back to platform",
				"project_id", projectID, "error", err)
		}
	}
	if !s.client.Configured() {
		_ = s.client.Send(ctx, to, subject, htmlBody) // logs instead of sending
		return Delivery{}, nil
	}
	id, err := s.client.SendWithID(ctx, to, subject, htmlBody)
	return Delivery{Via: ViaPlatform, MessageID: id}, err
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
//
// #258: redirectURL is the tenant-owned URL resolved by the caller
// (per-request override → auth_config default). It MUST be non-empty
// and already validated against the tenant's redirect_urls allowlist —
// this function trusts it. The link becomes `{redirectURL}?token=X`
// (or `&token=X` if the URL already has a query string), so a tenant
// can point at either `https://app.example.com/verify` or
// `https://app.example.com/callback?flow=verify`.
func (s *EmailService) SendVerificationEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail, redirectURL string) error {
	d, err := s.sendVerificationEmail(ctx, projectID, projectName, schemaName, userID, userEmail, redirectURL)
	s.recordSend(ctx, projectID, FlowVerification, userEmail, d, err)
	return err
}

func (s *EmailService) sendVerificationEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail, redirectURL string) (Delivery, error) {
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return Delivery{}, err
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
		return Delivery{}, fmt.Errorf("store verification token: %w", err)
	}

	customSubject, customHTML, err := s.loadCustomTemplate(ctx, projectID, "verification")
	if err != nil {
		slog.Warn("failed to load custom template, using default", "error", err)
	}

	actionURL := appendTokenQuery(redirectURL, rawToken)
	subject, body, err := RenderTemplate("verification", customSubject, customHTML, TemplateData{
		UserEmail:   userEmail,
		ProjectName: projectName,
		ActionURL:   actionURL,
		ExpiresIn:   "24 hours",
	})
	if err != nil {
		return Delivery{}, fmt.Errorf("render verification email: %w", err)
	}

	return s.sendProjectScoped(ctx, projectID, userEmail, subject, body)
}

// SendPasswordResetEmail sends a password reset link to the end-user.
// See SendVerificationEmail for the redirectURL contract.
func (s *EmailService) SendPasswordResetEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail, redirectURL string) error {
	d, err := s.sendPasswordResetEmail(ctx, projectID, projectName, schemaName, userID, userEmail, redirectURL)
	s.recordSend(ctx, projectID, FlowPasswordReset, userEmail, d, err)
	return err
}

func (s *EmailService) sendPasswordResetEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail, redirectURL string) (Delivery, error) {
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return Delivery{}, err
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
		return Delivery{}, fmt.Errorf("store password reset token: %w", err)
	}

	customSubject, customHTML, err := s.loadCustomTemplate(ctx, projectID, "password_reset")
	if err != nil {
		slog.Warn("failed to load custom template, using default", "error", err)
	}

	actionURL := appendTokenQuery(redirectURL, rawToken)
	subject, body, err := RenderTemplate("password_reset", customSubject, customHTML, TemplateData{
		UserEmail:   userEmail,
		ProjectName: projectName,
		ActionURL:   actionURL,
		ExpiresIn:   "1 hour",
	})
	if err != nil {
		return Delivery{}, fmt.Errorf("render password reset email: %w", err)
	}

	return s.sendProjectScoped(ctx, projectID, userEmail, subject, body)
}

// SendMagicLinkEmail sends a magic link sign-in email to the end-user.
// See SendVerificationEmail for the redirectURL contract.
func (s *EmailService) SendMagicLinkEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail, redirectURL string) error {
	d, err := s.sendMagicLinkEmail(ctx, projectID, projectName, schemaName, userID, userEmail, redirectURL)
	s.recordSend(ctx, projectID, FlowMagicLink, userEmail, d, err)
	return err
}

func (s *EmailService) sendMagicLinkEmail(ctx context.Context, projectID, projectName, schemaName, userID, userEmail, redirectURL string) (Delivery, error) {
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return Delivery{}, err
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
		return Delivery{}, fmt.Errorf("store magic link token: %w", err)
	}

	customSubject, customHTML, err := s.loadCustomTemplate(ctx, projectID, "magic_link")
	if err != nil {
		slog.Warn("failed to load custom template, using default", "error", err)
	}

	actionURL := appendTokenQuery(redirectURL, rawToken)
	subject, body, err := RenderTemplate("magic_link", customSubject, customHTML, TemplateData{
		UserEmail:   userEmail,
		ProjectName: projectName,
		ActionURL:   actionURL,
		ExpiresIn:   "15 minutes",
	})
	if err != nil {
		return Delivery{}, fmt.Errorf("render magic link email: %w", err)
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

// SendPlatformPasskeysClearedEmail notifies a console user that a
// password reset removed every passkey on their account (#621). No
// token — informational security notice. Satisfies the auth package's
// optional passkeyNoticeEmailer interface.
func (s *EmailService) SendPlatformPasskeysClearedEmail(ctx context.Context, userEmail string) error {
	subject, body, err := RenderPlatformTemplate("passkeys_cleared", TemplateData{
		UserEmail:   userEmail,
		ProjectName: "Eurobase Console",
		ActionURL:   s.consoleURL + "/account",
	})
	if err != nil {
		return fmt.Errorf("render passkeys-cleared email: %w", err)
	}
	return s.client.Send(ctx, userEmail, subject, body)
}

// SendPlatformVerificationEmail sends an email-verification link for a
// console (platform) user. Mirrors SendPlatformPasswordResetEmail: a
// 'verification' token in public.platform_email_tokens (24h, matching
// the tenant verification TTL) + a link to the console's /verify-email
// page. Platform mail always sends from us, never a tenant's BYO-SMTP.
func (s *EmailService) SendPlatformVerificationEmail(ctx context.Context, userID, userEmail string) error {
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO public.platform_email_tokens (user_id, token_hash, token_type, expires_at)
		 VALUES ($1, $2, 'verification', now() + interval '24 hours')`,
		userID, tokenHash,
	)
	if err != nil {
		return fmt.Errorf("store platform verification token: %w", err)
	}

	actionURL := fmt.Sprintf("%s/verify-email?token=%s", s.consoleURL, rawToken)
	subject, body, err := RenderTemplate("verification", "", "", TemplateData{
		UserEmail:   userEmail,
		ProjectName: "Eurobase Console",
		ActionURL:   actionURL,
		ExpiresIn:   "24 hours",
	})
	if err != nil {
		return fmt.Errorf("render platform verification email: %w", err)
	}

	return s.client.Send(ctx, userEmail, subject, body)
}

// OrgInvitationURL opens an org invitation in the console (after sign-in
// the console returns there); accepting still needs the invitee's own
// sign-in.
func OrgInvitationURL(consoleURL, invitationID string) string {
	return consoleURL + "/organizations?invitation=" + url.QueryEscape(invitationID)
}

// SendPlatformOrgInvitationEmail notifies a platform user that an
// org admin invited them to a Team-tier organization. The invitation
// is pending until they accept it in the console (Organizations),
// signed in as themselves. Platform mail always sends from us, never a
// tenant's BYO-SMTP.
//
// Fire-and-forget from the caller's perspective — a send failure
// (missing TEM creds in dev, provider hiccup) does NOT roll back
// the invite; the caller logs and moves on. #583.
func (s *EmailService) SendPlatformOrgInvitationEmail(ctx context.Context, invitedEmail, orgName, inviterEmail, invitationID string) error {
	actionURL := OrgInvitationURL(s.consoleURL, invitationID)
	subject, body, err := RenderTemplate("org_invitation", "", "", TemplateData{
		UserEmail:    invitedEmail,
		ProjectName:  "Eurobase Console",
		ActionURL:    actionURL,
		OrgName:      orgName,
		InviterEmail: inviterEmail,
	})
	if err != nil {
		return fmt.Errorf("render platform org invitation email: %w", err)
	}
	return s.client.Send(ctx, invitedEmail, subject, body)
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
	// Saved on a plan that included them, but the project has since
	// moved off it: send the default. A failed plan lookup keeps the
	// custom template (a paying project most likely; cosmetic either way).
	if s.templateGate != nil {
		if gerr := s.templateGate.CheckCustomTemplates(ctx, projectID); errors.Is(gerr, plans.ErrNotOnPlan) {
			return "", "", nil
		} else if gerr != nil {
			slog.Warn("custom templates: plan check failed, using the saved template", "project_id", projectID, "error", gerr)
		}
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

// appendTokenQuery appends `token=<raw>` as a query parameter to the
// tenant's redirect URL, respecting an existing query string AND an
// existing fragment.
//
// The fragment split is the tricky part. If the tenant configures
// `https://app.example.com/#/verify` (a hash-router SPA — Vue's
// createWebHashHistory, React Router's HashRouter, Angular's
// HashLocationStrategy), a naïve `url + "?token=X"` produces
// `https://app.example.com/#/verify?token=X`. Per WHATWG URL,
// everything after `#` is the fragment; the browser doesn't parse
// `?token=X` as a query param, and the tenant's
// `new URL(location).searchParams.get('token')` returns null. The
// user sees "invalid token" and every hash-router SPA is silently
// broken. Fix: split on the first `#`, insert `?token=X` (or
// `&token=X`) before it, re-attach the fragment.
//
// We deliberately do NOT round-trip through net/url — the redirect
// URL is already validated by the caller (URL-in-allowlist check on
// the AuthConfig side), and net/url percent-encoding would alter the
// tenant's URL in ways the tenant didn't expect. The token is hex
// (see generateToken above; always [0-9a-f]) and safe in a URL as-is.
func appendTokenQuery(redirectURL, rawToken string) string {
	// Split on the first `#` so any fragment stays at the end.
	base, frag, hasFrag := strings.Cut(redirectURL, "#")
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	base += sep + "token=" + rawToken
	if hasFrag {
		return base + "#" + frag
	}
	return base
}
