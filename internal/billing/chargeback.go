package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/jackc/pgx/v5"
)

// Chargebacks. Mollie calls the payment webhook again when the
// cardholder's bank reverses a payment; the payment stays "paid" and
// carries amountChargedBack. Without handling, the invoice stayed "paid"
// and the subscription kept charging the disputed card (each further
// chargeback costs a fee).
//
// On a full chargeback:
//   - the invoice becomes 'charged_back';
//   - if its subscription is live and the owner hasn't cancelled it,
//     the Mollie subscription is cancelled (whichever month was
//     disputed: card chargebacks usually arrive after the next renewal,
//     and further charges on the disputed card would bring further
//     chargebacks) and ours goes past_due: the owner gets the
//     payment-problem mail, can settle with Pay now (a new payment
//     method), and the usual grace ends on Free. canceled_at stays NULL
//     so Pay now accepts it;
//   - ops get an alert saying which of these happened. A subscription
//     already cancelled or ended is left to ops.
//
// A partial chargeback is left to ops (alert). If Mollie later reports
// the payment without amountChargedBack (the merchant won the dispute;
// Mollie doesn't document whether it does), the invoice goes back to
// 'paid' and ops are alerted; the subscription is not restarted.

// chargedBackCents is the amount Mollie reports as charged back.
func chargedBackCents(p *mollie.Payment) int {
	if p.AmountChargedBack == nil {
		return 0
	}
	return p.AmountChargedBack.Cents()
}

// processChargeback handles the chargeback side of a payment webhook.
// handled=true means the webhook is done: the normal paid/failed flow
// must not run for it.
func (s *Service) processChargeback(ctx context.Context, payment *mollie.Payment) (handled bool, err error) {
	total := payment.Amount.Cents()
	if payment.Status != "paid" || total <= 0 {
		return false, nil
	}
	if payment.AmountChargedBack == nil {
		return s.processChargebackReversal(ctx, payment)
	}
	cb := chargedBackCents(payment)
	if cb < total {
		slog.Error("billing.webhook.partial_chargeback", "mollie_payment_id", payment.ID,
			"charged_back_cents", cb, "amount_cents", total)
		s.alertOps("Partial chargeback",
			fmt.Sprintf("Payment `%s`: %s of %s charged back. Not handled automatically; check it in the Mollie dashboard.",
				payment.ID, payment.AmountChargedBack.Value, payment.Amount.Value))
		return true, nil
	}

	var (
		invoiceID, projectID, status string
		subscriptionID               *string
		paidAt                       *time.Time
	)
	err = s.pool.QueryRow(ctx,
		`SELECT id, project_id, subscription_id, status, paid_at
		   FROM public.invoices WHERE mollie_payment_id = $1`,
		payment.ID,
	).Scan(&invoiceID, &projectID, &subscriptionID, &status, &paidAt)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Error("billing.webhook.chargeback_without_invoice", "mollie_payment_id", payment.ID)
		s.alertOps("Chargeback on an unknown payment",
			fmt.Sprintf("Payment `%s` was charged back, but no invoice records it. Check it in the Mollie dashboard.", payment.ID))
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("load invoice for chargeback %s: %w", payment.ID, err)
	}
	switch status {
	case "charged_back":
		return true, nil // redelivery
	case "paid":
	default:
		// e.g. refunded before the bank reversed it: nothing to undo here.
		slog.Warn("billing.webhook.chargeback_on_unpaid_invoice",
			"mollie_payment_id", payment.ID, "invoice_status", status)
		return true, nil
	}

	// Is the subscription still live (and not cancelled by its owner)?
	var (
		live                bool
		subStatus           string
		mollieSub, mollieCu *string
	)
	if subscriptionID != nil {
		err = s.pool.QueryRow(ctx,
			`SELECT status IN ('active', 'past_due') AND canceled_at IS NULL, status,
			        mollie_subscription_id, mollie_customer_id
			   FROM public.subscriptions WHERE id = $1`,
			*subscriptionID,
		).Scan(&live, &subStatus, &mollieSub, &mollieCu)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return true, fmt.Errorf("load subscription for chargeback %s: %w", payment.ID, err)
		}
	}

	// Stop further charges on the disputed card first. Already stopped
	// is fine; any other failure fails the webhook so Mollie calls again
	// (nothing is written before this point).
	if live && mollieSub != nil && *mollieSub != "" && mollieCu != nil && *mollieCu != "" {
		if _, cerr := s.client.CancelSubscription(ctx, *mollieCu, *mollieSub); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
			cur, gerr := s.client.GetSubscription(ctx, *mollieCu, *mollieSub)
			if gerr != nil || !mollieSubscriptionStopped(cur.Status) {
				return true, fmt.Errorf("cancel subscription %s after chargeback: %w", *mollieSub, cerr)
			}
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return true, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`UPDATE public.invoices
		    SET status = 'charged_back', charged_back_at = COALESCE(charged_back_at, now())
		  WHERE id = $1 AND status = 'paid'`,
		invoiceID,
	); err != nil {
		return true, fmt.Errorf("mark invoice charged back: %w", err)
	}
	pastDue := false
	if live {
		tag, err := tx.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'past_due', past_due_since = COALESCE(past_due_since, now())
			  WHERE id = $1 AND status = 'active'`,
			*subscriptionID,
		)
		if err != nil {
			return true, fmt.Errorf("mark subscription past due after chargeback: %w", err)
		}
		pastDue = tag.RowsAffected() > 0
	}
	if err := tx.Commit(ctx); err != nil {
		return true, fmt.Errorf("commit chargeback: %w", err)
	}

	mailed := false
	if pastDue && mollieSub != nil && *mollieSub != "" {
		sendPaymentProblemMail(ctx, s.pool, s.invoiceMailer, *mollieSub, paymentProblemChargedBack)
		mailed = true
	}
	slog.Warn("billing.webhook.charged_back",
		"mollie_payment_id", payment.ID, "project_id", projectID,
		"subscription_live", live, "subscription_past_due", pastDue, "owner_mailed", mailed)
	s.alertOps("Chargeback",
		fmt.Sprintf("Payment `%s` (%s) for project `%s` was charged back; its invoice is marked charged back. %s",
			payment.ID, payment.Amount.Value, projectID, chargebackOutcome(subscriptionID != nil, live, pastDue, mailed, subStatus)))
	return true, nil
}

// chargebackOutcome describes for ops what a chargeback did to the
// subscription.
func chargebackOutcome(hasSub, live, pastDue, mailed bool, subStatus string) string {
	switch {
	case !hasSub:
		return "No subscription is linked to the invoice: check the project's plan by hand."
	case !live:
		return fmt.Sprintf("The subscription was already cancelled or has ended (status %s); nothing else changed: check whether the project should stay on its plan.", subStatus)
	case !pastDue:
		return "The Mollie subscription is cancelled; the subscription was already past due (no new mail to the owner)."
	case !mailed:
		return "The subscription is past due (no Mollie subscription to cancel); the owner was NOT mailed and can't use Pay now: handle it by hand."
	default:
		return "The Mollie subscription is cancelled and the subscription is past due: the owner was mailed and can use Pay now, else the project moves to Free after the grace period."
	}
}

// processChargebackReversal turns a charged-back invoice back to paid
// when Mollie no longer reports the payment as charged back. Not
// handled (false) for every other payment.
func (s *Service) processChargebackReversal(ctx context.Context, payment *mollie.Payment) (bool, error) {
	var projectID string
	err := s.pool.QueryRow(ctx,
		`UPDATE public.invoices SET status = 'paid'
		  WHERE mollie_payment_id = $1 AND status = 'charged_back'
		  RETURNING project_id`,
		payment.ID,
	).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("reverse chargeback %s: %w", payment.ID, err)
	}
	slog.Warn("billing.webhook.chargeback_reversed", "mollie_payment_id", payment.ID, "project_id", projectID)
	s.alertOps("Chargeback reversed",
		fmt.Sprintf("Payment `%s` for project `%s` is no longer charged back; its invoice is paid again. The subscription was not restarted: check the project's plan by hand.",
			payment.ID, projectID))
	return true, nil
}

// WithOpsAlerts posts billing alerts (chargebacks) to a Discord
// webhook. Empty disables them (logged only).
func (s *Service) WithOpsAlerts(discordWebhookURL string) *Service {
	if discordWebhookURL != "" {
		s.opsAlert = discordAlert(discordWebhookURL)
	}
	return s
}

func (s *Service) alertOps(title, description string) {
	if s.opsAlert != nil {
		s.opsAlert(title, description)
	}
}

// discordAlert posts one embed per alert in the background; a failure
// is logged without the webhook URL (it contains the token).
func discordAlert(webhookURL string) func(title, description string) {
	return func(title, description string) {
		body, err := json.Marshal(map[string]any{"embeds": []map[string]any{{
			"title":       "💳 " + title,
			"description": description,
			"color":       0xef4444,
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		}}})
		if err != nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				var ue *url.Error
				if errors.As(err, &ue) {
					err = ue.Err
				}
				slog.Warn("billing: ops alert failed", "error", err)
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 300 {
				slog.Warn("billing: ops alert rejected", "status", resp.StatusCode)
			}
		}()
	}
}
