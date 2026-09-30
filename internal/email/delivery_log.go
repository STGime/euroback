package email

// Per-project auth email log (migration 000135). The SDK auth endpoints
// answer "OK" whether or not an email went out, so they don't reveal
// which addresses have accounts (enumeration). This log is where a
// project's developers see what actually happened to each request:
// sent (and how), skipped (and why), or failed (and why).

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

// Auth email flows.
const (
	FlowVerification  = "verification"
	FlowPasswordReset = "password_reset"
	FlowMagicLink     = "magic_link"
)

// Outcomes.
const (
	OutcomeSent    = "sent"
	OutcomeSkipped = "skipped"
	OutcomeFailed  = "failed"
)

// Why an email was not sent (outcome "skipped") or failed.
const (
	// No user with this email in the project. Magic links, password
	// resets and verification resends only go to existing users.
	ReasonNoUser = "no_user"
	// Verification resend for an address that's already confirmed.
	ReasonAlreadyConfirmed = "already_confirmed"
	// The request's emailRedirectTo isn't in the allowed redirect URLs.
	ReasonRedirectRejected = "redirect_rejected"
	// No redirect URL configured for this email type.
	ReasonNoRedirect = "no_redirect_configured"
	// The platform has no email service configured (dev environments).
	ReasonEmailNotConfigured = "email_not_configured"
	// The project's own SMTP server didn't take the message.
	ReasonCustomSMTPFailed = "custom_smtp_failed"
	// The platform email service didn't take the message.
	ReasonPlatformFailed = "platform_failed"
	// The email couldn't be prepared (token storage, template).
	ReasonInternal = "internal_error"
)

// Delivery routes.
const (
	ViaPlatform   = "platform"
	ViaCustomSMTP = "custom_smtp"
)

// DeliveryLogRetention is how long entries are kept.
const DeliveryLogRetention = 14 * 24 * time.Hour

// maxDetailLen caps free text (errors, rejected URLs) per entry.
const maxDetailLen = 500

// maxDomainLen caps the domain kept by MaskEmail; a longer one isn't a
// real address (DNS names are at most 253 characters).
const maxDomainLen = 253

// Per-project write budget: the auth endpoints are public, so a flood of
// requests must not turn into a flood of rows (or push real entries out
// of view). Beyond it, entries are dropped — the email itself is
// unaffected.
const (
	logRatePerSecond = 1
	logBurst         = 60
)

// DeliveryLogEntry is one row of the log.
type DeliveryLogEntry struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Flow      string    `json:"flow"`
	Recipient string    `json:"recipient"`
	Outcome   string    `json:"outcome"`
	Reason    string    `json:"reason,omitempty"`
	Via       string    `json:"via,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// DeliveryLog writes entries. It holds the gateway's runtime pool, which
// may only INSERT into the table (000135).
type DeliveryLog struct {
	pool *pgxpool.Pool

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	dropped  map[string]time.Time // last "dropping" warning per project
}

// NewDeliveryLog returns a log writer; a nil pool gives a no-op log.
func NewDeliveryLog(pool *pgxpool.Pool) *DeliveryLog {
	if pool == nil {
		return nil
	}
	return &DeliveryLog{pool: pool, limiters: map[string]*rate.Limiter{}, dropped: map[string]time.Time{}}
}

// allow spends one entry of the project's write budget.
func (l *DeliveryLog) allow(projectID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.limiters[projectID]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(logRatePerSecond), logBurst)
		l.limiters[projectID] = lim
	}
	if lim.Allow() {
		return true
	}
	if time.Since(l.dropped[projectID]) > time.Minute {
		l.dropped[projectID] = time.Now()
		slog.Warn("email log: write budget exceeded, dropping entries", "project_id", projectID)
	}
	return false
}

// Record writes one entry, best effort: an auth request never fails or
// waits long because of its log line.
func (l *DeliveryLog) Record(ctx context.Context, projectID string, e DeliveryLogEntry) {
	if l == nil || projectID == "" || !l.allow(projectID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, err := l.pool.Exec(ctx,
		`INSERT INTO public.project_email_log (project_id, flow, recipient, outcome, reason, via, message_id, detail)
		 VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''))`,
		projectID, e.Flow, MaskEmail(e.Recipient), e.Outcome, e.Reason, e.Via, e.MessageID, truncate(e.Detail, maxDetailLen))
	if err != nil {
		slog.Warn("email log write failed", "project_id", projectID, "error", err)
	}
}

// ListDeliveryLog returns a project's newest entries first. Run it on
// the developer pool (the runtime role can't read the table).
func ListDeliveryLog(ctx context.Context, pool *pgxpool.Pool, projectID string, limit int) ([]DeliveryLogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := pool.Query(ctx,
		`SELECT id, created_at, flow, recipient, outcome,
		        COALESCE(reason, ''), COALESCE(via, ''), COALESCE(message_id, ''), COALESCE(detail, '')
		 FROM public.project_email_log
		 WHERE project_id = $1
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliveryLogEntry{}
	for rows.Next() {
		var e DeliveryLogEntry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Flow, &e.Recipient, &e.Outcome, &e.Reason, &e.Via, &e.MessageID, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CleanupDeliveryLog deletes entries older than DeliveryLogRetention, in
// batches (each its own statement, so no long lock after a busy day).
// Developer pool, like ListDeliveryLog.
func CleanupDeliveryLog(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var total int64
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		tag, err := pool.Exec(batchCtx,
			`DELETE FROM public.project_email_log WHERE id IN (
			   SELECT id FROM public.project_email_log
			   WHERE created_at < now() - make_interval(secs => $1)
			   LIMIT 10000)`,
			DeliveryLogRetention.Seconds())
		cancel()
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 10000 {
			return total, nil
		}
	}
}

// MaskEmail keeps the first character of the local part and the domain:
// "petra@perweg.de" → "p****@perweg.de". Enough to tell addresses and
// domains apart while debugging, without storing the full address —
// which, for a skipped request, may not belong to any user at all.
func MaskEmail(addr string) string {
	addr = strings.TrimSpace(addr)
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return "****"
	}
	local, domain := addr[:at], addr[at+1:]
	if len(domain) > maxDomainLen || !utf8.ValidString(domain) {
		return "(invalid address)"
	}
	if local == "" {
		return "****@" + domain
	}
	r, _ := utf8.DecodeRuneInString(local)
	return string(r) + "****@" + domain
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
