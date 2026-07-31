package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// HandleMollieWebhook receives Mollie's state-change POSTs. Body is
// application/x-www-form-urlencoded with a single `id` field; the
// canonical state is fetched by GET-ing the resource from Mollie
// (that's Mollie's trust model — the URL is a secret held only by
// us and Mollie, and unsigned POSTs from a third party fail closed
// at the GET step because Mollie doesn't recognise a foreign ID).
//
// Every terminal branch returns 200 — Mollie retries on non-2xx and
// we would rather log-and-continue than end up in a retry storm on
// a transient bug. Unrecoverable errors ARE logged with
// "billing.webhook.failed" so the log-based Grafana alert can fire.
func HandleMollieWebhook(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !svc.Enabled() {
			// Fail closed: Mollie shouldn't be pointing at a
			// disabled env, but if it is, returning 503 makes
			// Mollie retry — which turns "the flag is off" into
			// a webhook-queue backup. Return 200 + log.
			slog.Warn("billing.webhook.disabled_env_hit")
			w.WriteHeader(http.StatusOK)
			return
		}

		if err := r.ParseForm(); err != nil {
			slog.Warn("billing.webhook.parse_failed", "error", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		id := r.PostForm.Get("id")
		if id == "" {
			slog.Warn("billing.webhook.missing_id")
			w.WriteHeader(http.StatusOK)
			return
		}

		if err := svc.ProcessMollieWebhook(r.Context(), id); err != nil {
			// Every terminal error path also bumps a Prometheus
			// counter so the `BillingWebhookFailingSpike` alert
			// in deploy/k8s/cockpit/alerts.yaml can fire — the
			// slog line alone would be silent because we return
			// 200 to Mollie regardless (retry-storm avoidance).
			svc.incFailureMetric(resourceFromID(id))
			slog.Error("billing.webhook.failed",
				"id", id,
				"error", err,
			)
			// Still return 200. See docstring.
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// resourceFromID classifies a Mollie ID into a metric label so
// dashboards can bisect "payment failing" from "subscription
// failing" without eyeballing raw IDs.
func resourceFromID(id string) string {
	switch {
	case strings.HasPrefix(id, "tr_"):
		return "payment"
	case strings.HasPrefix(id, "sub_"):
		return "subscription"
	default:
		return "unknown"
	}
}

// ProcessMollieWebhook dispatches a webhook by ID prefix, fetches
// the canonical resource state from Mollie, and applies the
// resulting state transition to our DB. Idempotent — the same
// {id} may arrive multiple times (Mollie retries, or two IDs can
// arrive concurrently for the same subscription); every write is
// upsert-shaped or guarded by a status check.
//
// ID prefixes documented at https://docs.mollie.com/reference/:
//
//   tr_   — Payment
//   sub_  — Subscription
//   re_   — Refund (not handled here; refunds are created by our
//                  own action in PR 8, not webhook-driven)
//
// Anything else is a no-op with a warn — resilient to new Mollie
// resource types added after us.
func (s *Service) ProcessMollieWebhook(ctx context.Context, id string) error {
	switch {
	case strings.HasPrefix(id, "tr_"):
		return s.processPaymentWebhook(ctx, id)
	case strings.HasPrefix(id, "sub_"):
		return s.processSubscriptionWebhook(ctx, id)
	default:
		slog.Warn("billing.webhook.unknown_resource_type", "id", id)
		return nil
	}
}

// processPaymentWebhook handles /v2/payments state changes. There
// are three paths that matter:
//
//   1. sequenceType=first + status=paid    → activate: create the
//      Mollie recurring subscription against the mandate captured
//      by the first payment, flip our subscriptions row to
//      'active', mark the invoice paid, flip projects.plan.
//   2. sequenceType=recurring + status=paid → renewal: insert a
//      new invoice row, bump subscriptions.next_charge_at.
//   3. status=failed on any recurring/first attempt → mark the
//      subscription past_due (idempotent: only sets past_due_since
//      on the first transition, subsequent same-signal writes are
//      no-ops).
//
// The other Mollie payment states (open, pending, canceled,
// expired, authorized) are logged and ignored — we react to
// terminal transitions only.
func (s *Service) processPaymentWebhook(ctx context.Context, paymentID string) error {
	payment, err := s.client.GetPayment(ctx, paymentID)
	if err != nil {
		if errors.Is(err, mollie.ErrNotFound) {
			// Foreign ID (someone POSTing our webhook with a
			// random tr_… value). Not an error — the safety net
			// worked exactly as designed.
			slog.Info("billing.webhook.payment_not_found_at_mollie", "id", paymentID)
			return nil
		}
		return fmt.Errorf("fetch payment %s: %w", paymentID, err)
	}

	switch payment.Status {
	case "paid":
		if payment.SequenceType == mollie.SequenceTypeFirst {
			return s.activateFromFirstPayment(ctx, payment)
		}
		if payment.SequenceType == mollie.SequenceTypeRecurring {
			return s.recordRecurringPayment(ctx, payment)
		}
		// SequenceType=oneoff shouldn't happen for billing —
		// nothing in our flow creates oneoff payments. Log so we
		// spot a future refactor that accidentally introduces them.
		slog.Warn("billing.webhook.unexpected_paid_sequence", "id", payment.ID, "seq", payment.SequenceType)
		return nil

	case "failed", "canceled", "expired":
		return s.markPaymentFailure(ctx, payment)

	case "open", "pending", "authorized":
		// Non-terminal — Mollie will send another webhook when
		// the state resolves. No-op.
		return nil

	default:
		slog.Warn("billing.webhook.unknown_payment_status", "id", payment.ID, "status", payment.Status)
		return nil
	}
}

// activateFromFirstPayment is the critical transition: the user
// completed their first payment on Mollie's page, so a mandate
// now exists on the customer. We create the recurring Mollie
// subscription against that mandate, then flip our internal state
// atomically.
//
// Idempotent: if the subscription row is already 'active' with a
// mollie_subscription_id, we early-return. The CreateSubscription
// call itself is not idempotent (Mollie will happily create two
// subscriptions if we call twice), so the pre-check matters —
// belt-and-suspenders with the invoice paid_at guard below.
func (s *Service) activateFromFirstPayment(ctx context.Context, payment *mollie.Payment) error {
	subscriptionID := payment.Metadata["subscription_id"]
	projectID := payment.Metadata["project_id"]
	planCode := payment.Metadata["plan_code"]
	if subscriptionID == "" || projectID == "" || planCode == "" {
		return fmt.Errorf("first-payment webhook missing metadata: %+v", payment.Metadata)
	}
	if payment.MandateID == "" {
		// Shouldn't happen — Mollie always populates mandateId
		// on a paid first payment. Guard so a hypothetical
		// Mollie regression doesn't silently strand us.
		return fmt.Errorf("first-payment webhook has no mandateId (payment %s)", payment.ID)
	}

	// Check current state before doing external work.
	var currentStatus string
	var existingMollieSub *string
	err := s.pool.QueryRow(ctx,
		`SELECT status, mollie_subscription_id
		   FROM public.subscriptions
		  WHERE id = $1`,
		subscriptionID,
	).Scan(&currentStatus, &existingMollieSub)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("subscription %s not found (payment %s)", subscriptionID, payment.ID)
	}
	if err != nil {
		return fmt.Errorf("load subscription %s: %w", subscriptionID, err)
	}
	if currentStatus == "active" && existingMollieSub != nil && *existingMollieSub != "" {
		// Already activated — likely a webhook retry. Just make
		// sure the invoice is marked paid (idempotent below).
		return s.markInvoicePaid(ctx, payment)
	}

	// Create the Mollie recurring subscription. Amount + interval
	// derive from our subscriptions row (source of truth is our
	// DB — Mollie's payment amount is just the first charge).
	var priceCents int
	err = s.pool.QueryRow(ctx,
		`SELECT price_cents FROM public.subscriptions WHERE id = $1`,
		subscriptionID,
	).Scan(&priceCents)
	if err != nil {
		return fmt.Errorf("read price_cents for %s: %w", subscriptionID, err)
	}

	mollieSub, err := s.client.CreateSubscription(ctx, payment.CustomerID, mollie.SubscriptionCreateRequest{
		Amount:      mollie.AmountFromCents(priceCents, "EUR"),
		Interval:    "1 month",
		Description: payment.Description,
		// Pin the recurring subscription to the mandate captured
		// by THIS first payment. Without this, Mollie picks the
		// most-recent-valid mandate, which is ambiguous once a
		// customer has cycled through cancel-and-resubscribe
		// (multiple mandates on file). See PR 4 review.
		MandateID:  payment.MandateID,
		WebhookURL: fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		Metadata: map[string]string{
			"subscription_id": subscriptionID,
			"project_id":      projectID,
			"plan_code":       planCode,
		},
	}, mollie.WithIdempotencyKey("activate:"+subscriptionID))
	if err != nil {
		return fmt.Errorf("create mollie subscription: %w", err)
	}

	// Flip state atomically. next_charge_at from Mollie's
	// nextPaymentDate string (YYYY-MM-DD) — parse defensively.
	nextChargeAt := parseMollieDate(mollieSub.NextPaymentDate)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET mollie_subscription_id = $1,
		        status = 'active',
		        started_at = COALESCE(started_at, now()),
		        next_charge_at = $2
		  WHERE id = $3 AND status = 'incomplete'`,
		mollieSub.ID, nextChargeAt, subscriptionID,
	); err != nil {
		return fmt.Errorf("flip subscription to active: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.invoices
		    SET status = 'paid', paid_at = COALESCE(paid_at, now())
		  WHERE mollie_payment_id = $1`,
		payment.ID,
	); err != nil {
		return fmt.Errorf("mark invoice paid: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.projects SET plan = $1 WHERE id = $2`,
		planCode, projectID,
	); err != nil {
		return fmt.Errorf("flip project plan: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit activation: %w", err)
	}

	slog.Info("billing.webhook.activated",
		"subscription_id", subscriptionID,
		"project_id", projectID,
		"plan_code", planCode,
		"mollie_subscription_id", mollieSub.ID,
	)
	return nil
}

// recordRecurringPayment handles the happy path of a renewal:
// insert an invoice row for the new charge, bump next_charge_at
// on the subscription. Idempotent — a duplicate webhook for the
// same payment ID does nothing beyond the INSERT ... ON CONFLICT
// / next_charge_at re-computation.
func (s *Service) recordRecurringPayment(ctx context.Context, payment *mollie.Payment) error {
	if payment.SubscriptionID == "" {
		return fmt.Errorf("recurring payment missing subscriptionId (payment %s)", payment.ID)
	}

	// Look up our subscription row via Mollie's ID.
	var subscriptionID, projectID string
	var priceCents int
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, price_cents
		   FROM public.subscriptions
		  WHERE mollie_subscription_id = $1`,
		payment.SubscriptionID,
	).Scan(&subscriptionID, &projectID, &priceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no local subscription for mollie sub %s", payment.SubscriptionID)
	}
	if err != nil {
		return fmt.Errorf("load subscription: %w", err)
	}

	// Fetch canonical next_charge_at from Mollie (their
	// nextPaymentDate is authoritative — the client-side
	// "+1 month" arithmetic would drift on months of unequal
	// length).
	mollieSub, err := s.client.GetSubscription(ctx, payment.CustomerID, payment.SubscriptionID)
	if err != nil {
		// Non-fatal — the invoice insert is the important side
		// effect. Log and continue with next_charge_at unchanged.
		slog.Warn("billing.webhook.next_charge_refresh_failed",
			"subscription_id", subscriptionID, "error", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Insert invoice, ignoring duplicate delivery.
	tag, err := tx.Exec(ctx,
		`INSERT INTO public.invoices
		    (project_id, mollie_payment_id, amount_cents, currency, status, paid_at)
		 VALUES ($1, $2, $3, 'EUR', 'paid', now())
		 ON CONFLICT (mollie_payment_id) DO NOTHING`,
		projectID, payment.ID, priceCents,
	)
	if err != nil {
		return fmt.Errorf("insert recurring invoice: %w", err)
	}
	dupe := tag.RowsAffected() == 0

	// Clear past_due if a previous charge had failed and this
	// one succeeded (Mollie's automatic retry recovered).
	if _, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'active',
		        past_due_since = NULL,
		        next_charge_at = COALESCE($1, next_charge_at)
		  WHERE id = $2 AND status IN ('active', 'past_due')`,
		func() *time.Time {
			if mollieSub != nil {
				t := parseMollieDate(mollieSub.NextPaymentDate)
				if t != nil {
					return t
				}
			}
			return nil
		}(),
		subscriptionID,
	); err != nil {
		return fmt.Errorf("clear past_due: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit recurring: %w", err)
	}

	if !dupe {
		slog.Info("billing.webhook.recurring_paid",
			"subscription_id", subscriptionID,
			"payment_id", payment.ID,
			"amount_cents", priceCents,
		)
	}
	return nil
}

// markPaymentFailure flags the subscription past_due on the first
// failed charge. Mollie will retry twice more on its own schedule;
// each retry fires another webhook. Only the first transition
// sets past_due_since — the query below is a no-op on retries
// because it targets status='active'. PR 5's cron uses the aged
// past_due_since to time the 7-day grace before downgrade.
func (s *Service) markPaymentFailure(ctx context.Context, payment *mollie.Payment) error {
	if payment.SubscriptionID == "" {
		// A failed first payment (before mandate capture) doesn't
		// belong to a Mollie subscription yet — the user just
		// abandoned checkout. Their local 'incomplete' row will
		// be swept by PR 5's grace cron; nothing to do here.
		slog.Info("billing.webhook.first_payment_failed", "id", payment.ID)
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.invoices
			    SET status = 'failed'
			  WHERE mollie_payment_id = $1`,
			payment.ID,
		); err != nil {
			return fmt.Errorf("mark first invoice failed: %w", err)
		}
		return nil
	}

	res, err := s.pool.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'past_due',
		        past_due_since = now()
		  WHERE mollie_subscription_id = $1
		    AND status = 'active'`,
		payment.SubscriptionID,
	)
	if err != nil {
		return fmt.Errorf("mark past_due: %w", err)
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE public.invoices
		    SET status = 'failed'
		  WHERE mollie_payment_id = $1`,
		payment.ID,
	); err != nil {
		return fmt.Errorf("mark invoice failed: %w", err)
	}

	if res.RowsAffected() > 0 {
		slog.Warn("billing.webhook.past_due",
			"mollie_subscription_id", payment.SubscriptionID,
			"payment_id", payment.ID,
		)
	}
	return nil
}

// processSubscriptionWebhook handles /v2/subscriptions state
// changes. Mollie fires this when the subscription is canceled or
// completes its fixed term. Eurobase subscriptions have no fixed
// term (Times is unset), so "completed" is rare — but handle it
// for defensive completeness.
func (s *Service) processSubscriptionWebhook(ctx context.Context, subscriptionID string) error {
	// Look up the customer first so we can hit the correct API
	// path (Mollie's subscription GET is namespaced under a
	// customer). Our subscriptions row carries mollie_customer_id.
	var customerID string
	err := s.pool.QueryRow(ctx,
		`SELECT mollie_customer_id
		   FROM public.subscriptions
		  WHERE mollie_subscription_id = $1`,
		subscriptionID,
	).Scan(&customerID)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Info("billing.webhook.subscription_not_local", "id", subscriptionID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("load mollie_customer_id for sub %s: %w", subscriptionID, err)
	}

	mollieSub, err := s.client.GetSubscription(ctx, customerID, subscriptionID)
	if err != nil {
		if errors.Is(err, mollie.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("fetch subscription %s: %w", subscriptionID, err)
	}

	switch mollieSub.Status {
	case "canceled":
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'canceled',
			        canceled_at = COALESCE(canceled_at, now())
			  WHERE mollie_subscription_id = $1
			    AND status IN ('active', 'past_due')`,
			subscriptionID,
		); err != nil {
			return fmt.Errorf("mark canceled: %w", err)
		}
		slog.Info("billing.webhook.subscription_canceled", "id", subscriptionID)
	case "completed":
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'expired',
			        canceled_at = COALESCE(canceled_at, now())
			  WHERE mollie_subscription_id = $1
			    AND status IN ('active', 'past_due')`,
			subscriptionID,
		); err != nil {
			return fmt.Errorf("mark expired: %w", err)
		}
		slog.Info("billing.webhook.subscription_completed", "id", subscriptionID)
	case "active", "pending", "suspended":
		// No-op — active/pending are steady states; suspended
		// is Mollie's warning that recurring is temporarily
		// blocked (usually mandate issue). The payment webhook
		// already covers past_due tracking; we don't need to
		// mirror suspended into our schema.
		return nil
	default:
		slog.Warn("billing.webhook.unknown_subscription_status",
			"id", subscriptionID, "status", mollieSub.Status)
	}
	return nil
}

// markInvoicePaid is the idempotent invoice-flip used by webhook
// retries after activation is already complete. Extracted so both
// the "already active" fast path and future refunds hooks share
// it.
func (s *Service) markInvoicePaid(ctx context.Context, payment *mollie.Payment) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE public.invoices
		    SET status = 'paid',
		        paid_at = COALESCE(paid_at, now())
		  WHERE mollie_payment_id = $1`,
		payment.ID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return fmt.Errorf("mark invoice paid (%s): %w", pgErr.Code, err)
		}
		return fmt.Errorf("mark invoice paid: %w", err)
	}
	return nil
}

// parseMollieDate parses "YYYY-MM-DD" as returned by Mollie's
// nextPaymentDate + startDate fields. Returns nil on empty/parse
// failure so callers can treat the field as optional.
func parseMollieDate(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		slog.Warn("billing: malformed mollie date", "value", s, "error", err)
		return nil
	}
	return &t
}
