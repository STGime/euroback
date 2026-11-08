package billing

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// paymentProblemKind says why a Pro subscription went past_due.
type paymentProblemKind int

const (
	// A recurring charge failed. Mollie retries it on its own (up to
	// 5 times, once a day), so it may still be collected.
	paymentProblemChargeFailed paymentProblemKind = iota
	// Mollie stopped the subscription without a charge we heard about
	// (e.g. the mandate was revoked). Nothing will be retried.
	paymentProblemStoppedAtMollie
)

// rawMailer is the SendRaw surface shared by the invoice and
// downgrade mailers.
type rawMailer interface {
	SendRaw(ctx context.Context, to, subject, htmlBody string) error
}

// sendPaymentProblemMail tells the project owner that the Pro
// subscription is past due and when the project drops to Free.
// Best-effort: a failure is logged, never returned. Sent once per
// past_due transition (callers only send when their UPDATE moved the
// row from active to past_due).
func sendPaymentProblemMail(ctx context.Context, pool *pgxpool.Pool, mailer rawMailer, mollieSubscriptionID string, kind paymentProblemKind) {
	if mailer == nil || reflectIsNil(mailer) || pool == nil {
		return
	}
	var projectID, projectName, ownerEmail string
	var pastDueSince time.Time
	err := pool.QueryRow(ctx,
		`SELECT p.id, p.name, u.email, s.past_due_since
		   FROM public.subscriptions s
		   JOIN public.projects p ON p.id = s.project_id
		   JOIN public.platform_users u ON u.id = p.owner_id
		  WHERE s.mollie_subscription_id = $1
		    AND s.status = 'past_due'
		    AND s.past_due_since IS NOT NULL`,
		mollieSubscriptionID,
	).Scan(&projectID, &projectName, &ownerEmail, &pastDueSince)
	if err != nil {
		slog.Warn("billing: payment problem mail lookup failed",
			"mollie_subscription_id", mollieSubscriptionID, "error", err)
		return
	}
	if ownerEmail == "" {
		return
	}
	subject, body := renderPaymentProblemMail(projectID, projectName, pastDueSince.Add(pastDueGracePeriod), kind)
	if err := mailer.SendRaw(ctx, ownerEmail, subject, body); err != nil {
		slog.Warn("billing: payment problem mail send failed",
			"project_id", projectID, "error", err)
		return
	}
	slog.Info("billing: payment problem mail sent", "project_id", projectID, "kind", int(kind))
}

func renderPaymentProblemMail(projectID, projectName string, downgradeAt time.Time, kind paymentProblemKind) (subject, body string) {
	name := html.EscapeString(projectName)
	when := downgradeAt.UTC().Format("2 January 2006")
	billingURL := "https://console.eurobase.app/p/" + projectID + "/billing"

	subject = fmt.Sprintf("Eurobase — payment for %s failed", projectName)
	what := "We couldn't collect this month's payment for your Pro project <strong>" + name + "</strong>. " +
		"Our payment provider, Mollie, retries the charge automatically once a day for a few days. " +
		"The most common causes are an expired card, insufficient funds, or a bank that declined the charge."
	if kind == paymentProblemStoppedAtMollie {
		subject = fmt.Sprintf("Eurobase — the subscription for %s has stopped", projectName)
		what = "The Pro subscription for your project <strong>" + name + "</strong> was stopped by our payment provider, Mollie, " +
			"usually because the card or the payment authorisation behind it is no longer valid. No further charges will be attempted."
	}

	body = fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<body style="margin:0;padding:0;background-color:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#f3f4f6;padding:24px 0;">
    <tr><td align="center">
      <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;background:#ffffff;border-radius:12px;">
        <tr><td style="background:#1d4ed8;padding:24px 32px;color:#fff;">
          <p style="margin:0;font-size:20px;font-weight:700;">Eurobase</p>
          <p style="margin:6px 0 0;font-size:14px;color:#bfdbfe;">Payment notice</p>
        </td></tr>
        <tr><td style="padding:32px;color:#111827;">
          <p style="margin:0 0 16px;font-size:16px;">Hi,</p>
          <p style="margin:0 0 16px;font-size:15px;line-height:1.6;color:#374151;">%s</p>
          <p style="margin:0 0 16px;font-size:15px;line-height:1.6;color:#374151;">
            The project stays on Pro until <strong>%s</strong>. If the payment hasn't come through by then, it moves to the Free plan. Nothing is deleted: your database, storage, functions and users stay as they are.
          </p>
          <p style="margin:0 0 16px;font-size:14px;line-height:1.6;color:#374151;">
            To keep Pro, reply to this mail and we'll help you set up a new payment method. You can see the status on <a href="%s" style="color:#1d4ed8;">the project's billing page</a>.
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
</html>`, what, when, billingURL)
	return subject, body
}

// markStoppedAtMollie moves a live subscription to past_due when
// Mollie reports it as no longer running (canceled / suspended /
// completed / gone) although we never cancelled it. The past-due
// sweep then gives the usual grace before the project drops to Free.
//
// Subscriptions the owner cancelled (canceled_at set) are left alone:
// we cancel those at Mollie ourselves and the end-of-period sweep
// ends them. Returns whether the row changed.
func markStoppedAtMollie(ctx context.Context, pool *pgxpool.Pool, mollieSubscriptionID string) (bool, error) {
	tag, err := pool.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'past_due',
		        past_due_since = COALESCE(past_due_since, now())
		  WHERE mollie_subscription_id = $1
		    AND status = 'active'
		    AND canceled_at IS NULL`,
		mollieSubscriptionID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// mollieSubscriptionStopped reports whether a Mollie subscription
// status means no further charges will be made.
func mollieSubscriptionStopped(status string) bool {
	switch status {
	case "canceled", "suspended", "completed":
		return true
	}
	return false
}
