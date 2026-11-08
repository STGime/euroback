package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// purposePayNow marks the first payment an owner makes to settle an
// overdue subscription (metadata "purpose"). It charges one month's
// price and sets up a new mandate; the webhook then replaces the
// failing subscription with a new one on that mandate (applyPayNow).
const purposePayNow = "pay_overdue"

// ErrNotOverdue: Pay now is only for a subscription whose last charge
// failed and that hasn't been cancelled.
var ErrNotOverdue = errors.New("billing: the subscription has no overdue payment")

// StartPayNow creates a first payment of one month's price for a
// past-due subscription. The owner pays it on Mollie's page with any
// method that supports recurring payments; that also sets up the
// mandate the replacement subscription is charged to. Returns the
// Mollie checkout URL.
func (s *Service) StartPayNow(ctx context.Context, userID, projectID string) (string, error) {
	if !s.enabled {
		return "", ErrBillingDisabled
	}
	ps, err := s.loadPayerSubscription(ctx, userID, projectID)
	if err != nil {
		return "", err
	}
	if ps.Status != "past_due" || ps.CanceledAt != nil || ps.MollieSubID == "" {
		return "", ErrNotOverdue
	}
	if err := s.requireProfile(ctx, userID); err != nil {
		return "", err
	}
	var plan string
	var price *int
	if err := s.pool.QueryRow(ctx,
		`SELECT plan, price_cents FROM public.subscriptions WHERE id = $1`, ps.ID,
	).Scan(&plan, &price); err != nil {
		return "", fmt.Errorf("billing: load price: %w", err)
	}
	if price == nil || *price <= 0 {
		return "", fmt.Errorf("billing: subscription %s has no price", ps.ID)
	}

	// A random suffix, not a fixed key: a second click after an abandoned
	// payment must open a new payment, not return the abandoned one. Two
	// payments completed for the same overdue subscription are handled in
	// applyPayNow (the second is refunded).
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("billing: nonce: %w", err)
	}
	payment, err := s.client.CreatePayment(ctx, mollie.PaymentCreateRequest{
		Amount:       mollie.AmountFromCents(*price, "EUR"),
		Description:  fmt.Sprintf("Eurobase %s — %s", plan, ps.ProjectName),
		RedirectURL:  fmt.Sprintf("%s/p/%s/billing?status=pay_now", s.config.ConsoleBaseURL, projectID),
		WebhookURL:   fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		CustomerID:   ps.MollieCustID,
		SequenceType: mollie.SequenceTypeFirst,
		Metadata: map[string]string{
			"purpose":         purposePayNow,
			"subscription_id": ps.ID,
			"project_id":      projectID,
			"plan_code":       plan,
		},
	}, mollie.WithIdempotencyKey("pay-now:"+ps.ID+":"+hex.EncodeToString(nonce[:])))
	if err != nil {
		return "", fmt.Errorf("billing: create mollie payment: %w", err)
	}
	if payment.Links.Checkout == nil || payment.Links.Checkout.Href == "" {
		return "", fmt.Errorf("billing: mollie returned no checkout URL (payment %s)", payment.ID)
	}
	// The invoice for this payment; it moves to the replacement
	// subscription once the payment is in.
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO public.invoices
		    (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status, billed_to_user_id)
		 VALUES ($1, $2, $3, $4, 'EUR', 'pending', $5::uuid)
		 ON CONFLICT (mollie_payment_id) DO NOTHING`,
		projectID, ps.ID, payment.ID, *price, userID,
	); err != nil {
		return "", fmt.Errorf("billing: insert invoice: %w", err)
	}
	slog.Info("billing: pay now started",
		"subscription_id", ps.ID, "project_id", projectID, "mollie_payment_id", payment.ID)
	return payment.Links.Checkout.Href, nil
}

// processPayNowWebhook routes a webhook for a Pay now payment.
func (s *Service) processPayNowWebhook(ctx context.Context, payment *mollie.Payment) error {
	switch payment.Status {
	case "paid":
		return s.applyPayNow(ctx, payment)
	case "failed", "canceled", "expired":
		// Abandoned or declined: the subscription stays past due.
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.invoices SET status = 'failed' WHERE mollie_payment_id = $1 AND status = 'pending'`,
			payment.ID,
		); err != nil {
			return fmt.Errorf("mark pay-now invoice failed: %w", err)
		}
		return nil
	default:
		return nil
	}
}

// applyPayNow runs when a Pay now payment is paid. It replaces the
// past-due subscription with a new one on the mandate the payment set
// up: the old Mollie subscription is cancelled (which stops Mollie's
// retries of the failed charge), a new one starts a month from now
// (this payment covers the month from today), and locally the old row
// is expired and the new one inserted as active in one transaction.
//
// A late retry of the old charge that still succeeds lands on the
// expired row and is refunded (recordRecurringPayment).
//
// Refunded instead when the payment can't be used: the old charge was
// collected meanwhile, the owner cancelled, the payer is no longer the
// owner, or another subscription started.
func (s *Service) applyPayNow(ctx context.Context, payment *mollie.Payment) error {
	oldID := payment.Metadata["subscription_id"]
	projectID := payment.Metadata["project_id"]
	if oldID == "" || projectID == "" {
		slog.Error("billing.webhook.pay_now_missing_metadata", "mollie_payment_id", payment.ID)
		return nil
	}
	if alreadyRefunded(payment) {
		return nil // Mollie calling again for our refund
	}

	// Redelivery: the replacement subscription exists already.
	var existing string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM public.subscriptions WHERE mollie_payment_id = $1`, payment.ID,
	).Scan(&existing)
	if err == nil {
		return s.markInvoicePaid(ctx, payment)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check replacement for %s: %w", payment.ID, err)
	}
	if payment.MandateID == "" {
		return fmt.Errorf("pay-now payment has no mandateId (payment %s)", payment.ID)
	}

	var (
		status, plan       string
		price              *int
		canceledAt         *time.Time
		oldMollieSub       *string
		subCust, ownerCust *string
	)
	err = s.pool.QueryRow(ctx,
		`SELECT s.status, s.plan, s.price_cents, s.canceled_at, s.mollie_subscription_id,
		        s.mollie_customer_id, u.mollie_customer_id
		   FROM public.subscriptions s
		   JOIN public.projects p ON p.id = s.project_id
		   JOIN public.platform_users u ON u.id = p.owner_id
		  WHERE s.id = $1 AND s.project_id = $2 AND p.status = 'active'`,
		oldID, projectID,
	).Scan(&status, &plan, &price, &canceledAt, &oldMollieSub, &subCust, &ownerCust)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.refundPayNow(ctx, payment, "subscription or project not found")
	}
	if err != nil {
		return fmt.Errorf("load subscription %s: %w", oldID, err)
	}
	payerIsOwner := subCust != nil && ownerCust != nil &&
		*subCust == payment.CustomerID && *ownerCust == payment.CustomerID
	// expired: the grace period ran out and the project was moved to Free
	// while the owner was paying. The payment still brings Pro back.
	usable := (status == "past_due" && canceledAt == nil) || status == "expired"
	if !payerIsOwner || !usable || price == nil {
		slog.Warn("billing.webhook.pay_now_refused",
			"subscription_id", oldID, "status", status, "canceled", canceledAt != nil,
			"payer_is_owner", payerIsOwner, "mollie_payment_id", payment.ID)
		return s.refundPayNow(ctx, payment, "the overdue subscription can no longer be settled with this payment")
	}

	// Stop the old subscription first, so Mollie stops retrying the
	// failed charge. Already stopped is fine; anything else is retried
	// with the webhook (Mollie calls again).
	if status == "past_due" && oldMollieSub != nil && *oldMollieSub != "" {
		if _, cerr := s.client.CancelSubscription(ctx, *subCust, *oldMollieSub); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
			cur, gerr := s.client.GetSubscription(ctx, *subCust, *oldMollieSub)
			if gerr != nil || !mollieSubscriptionStopped(cur.Status) {
				return fmt.Errorf("cancel overdue mollie subscription %s: %w", *oldMollieSub, cerr)
			}
		}
	}

	newID := uuid.NewString()
	mollieSub, err := s.client.CreateSubscription(ctx, payment.CustomerID, mollie.SubscriptionCreateRequest{
		Amount:      mollie.AmountFromCents(*price, "EUR"),
		Interval:    "1 month",
		StartDate:   mollie.DefaultRecurringStartDate(time.Now()),
		Description: payment.Description,
		MandateID:   payment.MandateID,
		WebhookURL:  fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		Metadata: map[string]string{
			"subscription_id": newID,
			"project_id":      projectID,
			"plan_code":       plan,
		},
	}, mollie.WithIdempotencyKey("pay-now-activate:"+payment.ID))
	if err != nil {
		return fmt.Errorf("create replacement mollie subscription: %w", err)
	}

	committed, err := s.replaceOverdueSubscription(ctx, payment, oldID, newID, projectID, plan, *price, mollieSub)
	if err != nil {
		return err
	}
	if !committed {
		// Closed or replaced while we were working: undo at Mollie.
		s.abandonActivation(ctx, payment, mollieSub.ID, newID)
		return nil
	}
	slog.Info("billing.webhook.pay_now_settled",
		"old_subscription_id", oldID, "subscription_id", newID, "project_id", projectID,
		"mollie_subscription_id", mollieSub.ID, "mollie_payment_id", payment.ID)
	if invoiceID := s.invoiceIDForPayment(ctx, payment.ID); invoiceID != "" {
		s.enqueueInvoiceRender(invoiceID)
	}
	return nil
}

// replaceOverdueSubscription is applyPayNow's local switch: expire the
// old row, insert the new active one, mark the invoice paid and put the
// project (back) on the plan. Returns false when it can't apply (the
// old row changed, or another live subscription exists).
func (s *Service) replaceOverdueSubscription(ctx context.Context, payment *mollie.Payment,
	oldID, newID, projectID, plan string, price int, mollieSub *mollie.Subscription) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	closed, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'expired', canceled_at = COALESCE(canceled_at, now())
		  WHERE id = $1 AND (status = 'expired' OR (status = 'past_due' AND canceled_at IS NULL))`,
		oldID,
	)
	if err != nil {
		return false, fmt.Errorf("expire overdue subscription: %w", err)
	}
	if closed.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO public.subscriptions
		    (id, project_id, mollie_customer_id, mollie_subscription_id, mollie_payment_id,
		     plan, price_cents, currency, billing_interval, status, started_at, next_charge_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'EUR', '1 month', 'active', now(), $8)`,
		newID, projectID, payment.CustomerID, mollieSub.ID, payment.ID, plan, price, firstChargeDate(mollieSub),
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return false, nil // another live subscription started meanwhile
		}
		return false, fmt.Errorf("insert replacement subscription: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE public.invoices
		    SET status = 'paid', paid_at = COALESCE(paid_at, now()), subscription_id = $2
		  WHERE mollie_payment_id = $1`,
		payment.ID, newID,
	); err != nil {
		return false, fmt.Errorf("mark pay-now invoice paid: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE public.projects SET plan = $2, legacy_pro_grace_until = NULL WHERE id = $1`,
		projectID, plan,
	); err != nil {
		return false, fmt.Errorf("restore project plan: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit pay now: %w", err)
	}
	return true, nil
}

// refundPayNow refunds a Pay now payment that can't be applied and
// marks its invoice refunded.
func (s *Service) refundPayNow(ctx context.Context, payment *mollie.Payment, reason string) error {
	if err := s.refundOrphanedPayment(ctx, payment, reason); err != nil {
		// Fail the webhook so Mollie calls again; the refund's
		// idempotency key makes the retry safe.
		return fmt.Errorf("refund pay-now payment %s: %w", payment.ID, err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE public.invoices SET status = 'refunded' WHERE mollie_payment_id = $1`, payment.ID,
	); err != nil {
		slog.Warn("billing.webhook.pay_now_invoice_update_failed", "mollie_payment_id", payment.ID, "error", err)
	}
	return nil
}

// HandleStartPayNow is POST /platform/billing/projects/{project_id}/pay-now.
// Returns {"checkout_url": "..."}.
func HandleStartPayNow(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		projectID := chi.URLParam(r, "project_id")
		url, err := svc.StartPayNow(r.Context(), claims.Subject, projectID)
		if err != nil {
			switch {
			case errors.Is(err, ErrNotOverdue):
				writeJSONError(w, http.StatusConflict, "not_overdue", "this project has no overdue payment")
			case errors.Is(err, ErrBillingProfileRequired):
				writeJSONError(w, http.StatusConflict, "billing_profile_required", "please add your billing details before continuing")
			default:
				writePaymentMethodError(w, err, claims.Subject, projectID)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"checkout_url": url})
	}
}
