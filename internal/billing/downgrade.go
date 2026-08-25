package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/jackc/pgx/v5/pgxpool"
)

// downgradeInterval is how often RunDowngradeSweep runs. Hourly
// gives 24 chances per day for either branch to fire; the actual
// downgrade timing precision is "within the hour after grace
// elapses", which is fine for a user-visible day-scale window.
const downgradeInterval = 1 * time.Hour

// pastDueGracePeriod is how long a subscription stays in past_due
// before we downgrade the project. 7 days after Mollie's own 3-
// strike retry schedule (~21 days) means ~28 days from first
// failed charge to auto-downgrade. Documented in the plan.
const pastDueGracePeriod = 7 * 24 * time.Hour

// downgradeMailer is the surface DowngradeService uses to send
// the "your Pro subscription expired" mail. Kept as an interface
// so tests can inject a no-op recorder and this package doesn't
// import internal/email directly (avoids a cycle).
type downgradeMailer interface {
	SendRaw(ctx context.Context, to, subject, htmlBody string) error
}

// DowngradeService runs the periodic sweep that finds Pro
// projects the platform should drop back to Free, and does the
// drop-back. Two triggers:
//
//   1. subscriptions.status = 'past_due' for longer than
//      pastDueGracePeriod. A user whose card fails and doesn't
//      get replaced during Mollie's retry window is billed out.
//   2. projects.plan = 'pro' with legacy_pro_grace_until in the
//      past AND no live subscription. Legacy beta-Pro users who
//      didn't add a card during the 14-day window (migration
//      000080).
//
// The downgrade action itself is shared:
//   - Cancel the Mollie subscription (if we have one on file).
//   - Flip subscriptions.status = 'expired', canceled_at = now().
//   - Flip projects.plan = 'free', clear legacy_pro_grace_until,
//     reset last_active_at so the 30-day idle-pause clock starts
//     fresh (kinder than "you're on Free and pause tomorrow").
//   - Send a heads-up mail to the project owner.
type DowngradeService struct {
	pool    *pgxpool.Pool
	client  *mollie.Client
	mailer  downgradeMailer
	from    string // rendered "From: X" for the notification mail
}

// NewDowngradeService constructs the service. `mailer` may be nil
// in dev environments (mail send is best-effort, downgrade
// completes without it). Empty `from` falls back to a sensible
// default derived from the notification subject.
func NewDowngradeService(pool *pgxpool.Pool, client *mollie.Client, mailer downgradeMailer, from string) *DowngradeService {
	return &DowngradeService{pool: pool, client: client, mailer: mailer, from: from}
}

// StartLoop launches the hourly downgrade sweep in a goroutine.
// Returns immediately; the loop exits when ctx is cancelled.
// Errors from a single tick are logged but never propagated — the
// sweep is idempotent and the next tick reconciles.
//
// Same shape as StartAuditRetention: goroutine + ticker, not a
// River job. This is fixed-cadence housekeeping without an
// upstream trigger, so River's HTTP-triggered semantics buy
// nothing.
func (s *DowngradeService) StartLoop(ctx context.Context) {
	go func() {
		// Fire once on startup so a fresh deploy processes any
		// backlog immediately rather than waiting up to an hour.
		s.RunSweep(ctx)

		ticker := time.NewTicker(downgradeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunSweep(ctx)
			}
		}
	}()
	slog.Info("billing: downgrade sweep loop started", "interval", downgradeInterval)
}

// RunSweep processes three branches. Exposed for manual
// invocation from an ops runbook and for the eventual
// integration test. Never returns error — each candidate row is
// best-effort; a single failure logs and the loop continues to
// the next.
//
// Branches:
//   1. past-due-grace-elapsed (PR 5) — recurring charge failed,
//      Mollie's retry window is up.
//   2. legacy-Pro-grace-elapsed (PR 5) — beta-Pro user never
//      added a card within 14 days.
//   3. end-of-period cancel (PR 8) — user canceled with
//      end_of_period mode; canceled_at stamped, next_charge_at
//      passed. Without this branch the project would stay on
//      Pro indefinitely — the PR 8 review caught this before
//      merge.
func (s *DowngradeService) RunSweep(ctx context.Context) {
	pastDue, err := s.findPastDueGraceElapsed(ctx)
	if err != nil {
		slog.Error("billing: past-due sweep query failed", "error", err)
	} else {
		for _, c := range pastDue {
			s.downgradeOne(ctx, c, "past_due_grace_elapsed")
		}
	}

	legacy, err := s.findLegacyProGraceElapsed(ctx)
	if err != nil {
		slog.Error("billing: legacy-pro sweep query failed", "error", err)
	} else {
		for _, c := range legacy {
			s.downgradeOne(ctx, c, "legacy_pro_grace_elapsed")
		}
	}

	endOfPeriod, err := s.findEndOfPeriodElapsed(ctx)
	if err != nil {
		slog.Error("billing: end-of-period sweep query failed", "error", err)
	} else {
		for _, c := range endOfPeriod {
			s.downgradeOne(ctx, c, "end_of_period_cancel")
		}
	}
}

// downgradeCandidate is the row shape the two find* queries
// return. Not exported — internal only.
type downgradeCandidate struct {
	ProjectID              string
	ProjectName            string
	OwnerEmail             string
	SubscriptionID         *string // nil for legacy-Pro (no sub)
	MollieSubscriptionID   *string
	MollieCustomerID       *string
}

// findPastDueGraceElapsed returns projects whose subscription has
// been past_due longer than pastDueGracePeriod.
func (s *DowngradeService) findPastDueGraceElapsed(ctx context.Context) ([]downgradeCandidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.name, u.email,
		        s.id, s.mollie_subscription_id, s.mollie_customer_id
		   FROM public.subscriptions s
		   JOIN public.projects p ON p.id = s.project_id
		   JOIN public.platform_users u ON u.id = p.owner_id
		  WHERE s.status = 'past_due'
		    AND s.past_due_since < now() - $1::interval
		  ORDER BY s.past_due_since ASC
		  LIMIT 500`,
		fmt.Sprintf("%d hours", int(pastDueGracePeriod.Hours())),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []downgradeCandidate
	for rows.Next() {
		var c downgradeCandidate
		var subID string
		if err := rows.Scan(&c.ProjectID, &c.ProjectName, &c.OwnerEmail,
			&subID, &c.MollieSubscriptionID, &c.MollieCustomerID); err != nil {
			return nil, err
		}
		c.SubscriptionID = &subID
		out = append(out, c)
	}
	return out, rows.Err()
}

// findEndOfPeriodElapsed returns subscriptions the user cancelled
// with end_of_period mode whose next_charge_at has now passed.
// PR 8 branch. Guarded by canceled_at IS NOT NULL so an active
// subscription with a future next_charge_at doesn't get swept.
func (s *DowngradeService) findEndOfPeriodElapsed(ctx context.Context) ([]downgradeCandidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.name, u.email,
		        s.id, s.mollie_subscription_id, s.mollie_customer_id
		   FROM public.subscriptions s
		   JOIN public.projects p ON p.id = s.project_id
		   JOIN public.platform_users u ON u.id = p.owner_id
		  WHERE s.status = 'active'
		    AND s.canceled_at IS NOT NULL
		    AND s.next_charge_at IS NOT NULL
		    AND s.next_charge_at < now()
		  ORDER BY s.canceled_at ASC
		  LIMIT 500`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []downgradeCandidate
	for rows.Next() {
		var c downgradeCandidate
		var subID string
		if err := rows.Scan(&c.ProjectID, &c.ProjectName, &c.OwnerEmail,
			&subID, &c.MollieSubscriptionID, &c.MollieCustomerID); err != nil {
			return nil, err
		}
		c.SubscriptionID = &subID
		out = append(out, c)
	}
	return out, rows.Err()
}

// findLegacyProGraceElapsed returns projects marked Pro at
// migration 000080 that never got a subscription within the
// 14-day window. The "no live subscription" guard prevents
// double-downgrading a user who paid on grace-day 13 hour 23 and
// then hit this sweep on grace-day 14 hour 1 (the plan-doc race).
func (s *DowngradeService) findLegacyProGraceElapsed(ctx context.Context) ([]downgradeCandidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.name, u.email
		   FROM public.projects p
		   JOIN public.platform_users u ON u.id = p.owner_id
		  WHERE p.plan = 'pro'
		    AND p.legacy_pro_grace_until IS NOT NULL
		    AND p.legacy_pro_grace_until < now()
		    AND (p.comped_until IS NULL OR p.comped_until < now())
		    AND NOT EXISTS (
		         SELECT 1 FROM public.subscriptions s
		          WHERE s.project_id = p.id
		            AND s.status IN ('incomplete', 'active', 'past_due')
		    )
		  ORDER BY p.legacy_pro_grace_until ASC
		  LIMIT 500`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []downgradeCandidate
	for rows.Next() {
		var c downgradeCandidate
		if err := rows.Scan(&c.ProjectID, &c.ProjectName, &c.OwnerEmail); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// downgradeOne performs the flip for one project. Order matters:
//
//   1. Cancel the Mollie subscription FIRST (best-effort — if
//      Mollie is down we still want to stop billing the user).
//   2. Update our DB in a tx: subscription → expired, project →
//      free + clear grace + reset last_active_at.
//   3. Send the notification mail (best-effort).
//
// reason is a slog label so operators can bisect "why did project
// X get downgraded" without SQL archaeology.
func (s *DowngradeService) downgradeOne(ctx context.Context, c downgradeCandidate, reason string) {
	// 1. Cancel Mollie subscription (only for past_due branch —
	// legacy-Pro has no mollie_subscription_id).
	if c.MollieSubscriptionID != nil && c.MollieCustomerID != nil &&
		*c.MollieSubscriptionID != "" && *c.MollieCustomerID != "" {
		_, err := s.client.CancelSubscription(ctx, *c.MollieCustomerID, *c.MollieSubscriptionID)
		if err != nil && !errors.Is(err, mollie.ErrNotFound) {
			// ErrNotFound is fine — subscription already gone
			// on Mollie's side. Any other error we log and
			// press on; the local downgrade must proceed so
			// we stop charging semantics.
			slog.Warn("billing: mollie cancel failed during downgrade",
				"project_id", c.ProjectID,
				"mollie_subscription_id", *c.MollieSubscriptionID,
				"error", err,
			)
		}
	}

	// 2. Flip local state.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		slog.Error("billing: downgrade begin tx failed", "project_id", c.ProjectID, "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if c.SubscriptionID != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'expired',
			        canceled_at = COALESCE(canceled_at, now())
			  WHERE id = $1
			    AND status IN ('active', 'past_due', 'incomplete')`,
			*c.SubscriptionID,
		); err != nil {
			slog.Error("billing: downgrade subscription update failed",
				"project_id", c.ProjectID, "error", err)
			return
		}
	}

	// The comp guard is defence-in-depth: Branch B already excludes
	// comped rows at the SELECT, but Branches A (past_due) and C
	// (end_of_period) don't — a comped project that somehow acquired
	// a subscription (e.g. a future superadmin/UI comp on a paying
	// project) must not silently drop to Free on cancel or churn.
	// Enforce comp at the choke-point so every branch, present and
	// future, is honoured. 0-row-affected below covers both the
	// "another sweep got here first" AND the "under active comp"
	// cases; log message reflects both.
	projectRes, err := tx.Exec(ctx,
		`UPDATE public.projects
		    SET plan = 'free',
		        legacy_pro_grace_until = NULL,
		        last_active_at = now()
		  WHERE id = $1
		    AND plan = 'pro'
		    AND (comped_until IS NULL OR comped_until < now())`,
		c.ProjectID,
	)
	if err != nil {
		slog.Error("billing: downgrade project update failed",
			"project_id", c.ProjectID, "error", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("billing: downgrade commit failed",
			"project_id", c.ProjectID, "error", err)
		return
	}

	// If the project UPDATE affected 0 rows, another actor already
	// downgraded this project (typical cause: two gateway pods both
	// hit the startup-fire sweep simultaneously — subscription
	// UPDATE is also 0-row-safe via its status guard, but without
	// this check we'd log "downgraded" AND send the notification
	// mail twice). Skip both here; the winning actor has already
	// done them.
	if projectRes.RowsAffected() == 0 {
		slog.Debug("billing: downgrade skipped — project already Free (concurrent sweep) or under active comp",
			"project_id", c.ProjectID)
		return
	}

	slog.Info("billing: downgraded to free",
		"project_id", c.ProjectID,
		"project_name", c.ProjectName,
		"reason", reason,
	)

	// 3. Send notification mail (best-effort — a mail failure
	// doesn't roll back the downgrade).
	s.sendDowngradeMail(ctx, c, reason)
}

// sendDowngradeMail renders and sends the "your Pro subscription
// has ended" mail. Content is a plain inline-styled HTML letter,
// same shape as the beta-update mails; kept short — this is a
// heads-up, not a marketing pitch. Best-effort: mail failures log
// but do not roll back the downgrade.
func (s *DowngradeService) sendDowngradeMail(ctx context.Context, c downgradeCandidate, reason string) {
	if s.mailer == nil || c.OwnerEmail == "" {
		return
	}

	subject := fmt.Sprintf("Eurobase — %s is on the Free plan", c.ProjectName)

	reasonBlurb := "your Pro subscription payment failed and the retry window has ended"
	if reason == "legacy_pro_grace_elapsed" {
		reasonBlurb = "the 14-day window to add a payment method to your closed-beta Pro project has ended"
	}

	body := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<body style="margin:0;padding:0;background-color:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#f3f4f6;padding:24px 0;">
    <tr><td align="center">
      <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;background:#ffffff;border-radius:12px;">
        <tr><td style="background:#1d4ed8;padding:24px 32px;color:#fff;">
          <p style="margin:0;font-size:20px;font-weight:700;">Eurobase</p>
          <p style="margin:6px 0 0;font-size:14px;color:#bfdbfe;">Subscription notice</p>
        </td></tr>
        <tr><td style="padding:32px;color:#111827;">
          <p style="margin:0 0 16px;font-size:16px;">Hi,</p>
          <p style="margin:0 0 16px;font-size:15px;line-height:1.6;color:#374151;">
            Your project <strong>%s</strong> is now on the Free plan &mdash; %s.
          </p>
          <p style="margin:0 0 16px;font-size:14px;line-height:1.6;color:#374151;">
            Nothing is deleted. Your database, storage, functions and users are all still there. The Free plan caps you at 5,000 monthly active users, 512 MB storage, 2 GB bandwidth, 50 realtime connections. The project also auto-pauses after 30 days of inactivity; the first request after that wakes it up in ~30 seconds.
          </p>
          <p style="margin:0 0 16px;font-size:14px;line-height:1.6;color:#374151;">
            If this wasn't intentional, or you'd like to re-enable Pro (&euro;19/mo per project), reply to this mail or head to <a href="https://console.eurobase.app/p/%s/billing" style="color:#1d4ed8;">the project's billing page</a>.
          </p>
          <p style="margin:20px 0 6px;font-size:14px;color:#374151;">Thanks,<br><strong>Stefan</strong></p>
        </td></tr>
        <tr><td style="padding:20px 32px 28px;border-top:1px solid #e5e7eb;">
          <p style="margin:0;font-size:12px;color:#9ca3af;">
            Eurobase O&Uuml;, Ahtri 12, Tallinn 15551, Estonia &middot; <a href="https://www.eurobase.app" style="color:#6b7280;">eurobase.app</a>
          </p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`, c.ProjectName, reasonBlurb, c.ProjectID)

	if err := s.mailer.SendRaw(ctx, c.OwnerEmail, subject, body); err != nil {
		slog.Warn("billing: downgrade mail send failed",
			"project_id", c.ProjectID, "email", c.OwnerEmail, "error", err)
	}
}
