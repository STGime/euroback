package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/mailaddr"
	"github.com/jackc/pgx/v5/pgxpool"
)

// failedEmailLister is the email provider's failed-email list
// (email.EmailClient.ListFailedEmails).
type failedEmailLister interface {
	ListFailedEmails(ctx context.Context, since time.Time) ([]email.FailedEmail, error)
}

const (
	emailBounceSyncInterval = time.Hour
	// Each pass looks back further than the interval, so a missed or
	// failed pass is covered by the next one. Upserts are idempotent.
	emailBounceLookback = 26 * time.Hour
	// The first pass after a start catches up on recent history.
	emailBounceBackfill = 30 * 24 * time.Hour
)

// StartEmailBounceSync keeps public.email_suppressions in step with the
// email provider's failed emails, so bulk announcements and the
// onboarding series stop going to addresses that bounce. Only addresses
// of platform accounts or allowlist entries are recorded (end-user mail
// a project sends through the platform provider is not kept), and rows
// whose address belongs to neither any more are removed.
func StartEmailBounceSync(ctx context.Context, pool *pgxpool.Pool, lister *email.EmailClient) {
	if lister == nil || !lister.Configured() {
		slog.Info("email bounce sync disabled: email client not configured")
		return
	}
	go func() {
		syncEmailBounces(ctx, pool, lister, time.Now().Add(-emailBounceBackfill))
		ticker := time.NewTicker(emailBounceSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				syncEmailBounces(ctx, pool, lister, time.Now().Add(-emailBounceLookback))
			}
		}
	}()
}

// syncEmailBounces runs one pass and returns how many addresses it
// recorded (new or refreshed). Errors are logged; the next pass retries.
func syncEmailBounces(ctx context.Context, pool *pgxpool.Pool, lister failedEmailLister, since time.Time) int {
	// Cleanup runs even when the listing fails, so addresses of deleted
	// accounts never linger.
	defer removeStaleSuppressions(ctx, pool)
	failed, err := lister.ListFailedEmails(ctx, since)
	if err != nil {
		slog.Warn("email bounce sync: listing failed emails", "error", err)
		return 0
	}
	recorded := 0
	for _, f := range failed {
		if mailaddr.IsReserved(f.Recipient) {
			continue // never platform recipients worth tracking
		}
		at := f.At
		if at.IsZero() {
			at = time.Now()
		}
		tag, err := pool.Exec(ctx, `
			INSERT INTO public.email_suppressions (email, reason, first_seen_at, last_seen_at)
			SELECT $1, $2, $3, $3
			 WHERE EXISTS (SELECT 1 FROM public.platform_users WHERE lower(email) = $1)
			    OR EXISTS (SELECT 1 FROM public.platform_allowlist WHERE lower(trim(email)) = $1)
			ON CONFLICT (email) DO UPDATE
			   SET reason = EXCLUDED.reason,
			       last_seen_at = GREATEST(public.email_suppressions.last_seen_at, EXCLUDED.last_seen_at)`,
			f.Recipient, f.Reason, at)
		if err != nil {
			slog.Warn("email bounce sync: recording suppression", "error", err)
			continue
		}
		recorded += int(tag.RowsAffected())
	}
	if recorded > 0 {
		slog.Info("email bounce sync: suppressions recorded", "count", recorded)
	}
	return recorded
}

// emailSuppressionTTL: a suppression not refreshed by a new bounce for
// this long is dropped, so a mailbox that comes back gets mail again.
const emailSuppressionTTL = 90 * 24 * time.Hour

// removeStaleSuppressions drops rows whose address no longer belongs to a
// platform account or allowlist entry, and rows not seen for
// emailSuppressionTTL.
func removeStaleSuppressions(ctx context.Context, pool *pgxpool.Pool) {
	if _, err := pool.Exec(ctx, `
		DELETE FROM public.email_suppressions s
		 WHERE s.last_seen_at < now() - make_interval(secs => $1)
		    OR (NOT EXISTS (SELECT 1 FROM public.platform_users u WHERE lower(u.email) = s.email)
		        AND NOT EXISTS (SELECT 1 FROM public.platform_allowlist a WHERE lower(trim(a.email)) = s.email))`,
		emailSuppressionTTL.Seconds()); err != nil {
		slog.Warn("email bounce sync: removing stale suppressions", "error", err)
	}
}
