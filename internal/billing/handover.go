package billing

// Billing handover on a project ownership transfer (#846).
//
// The previous owner has paid up to next_charge_at. On transfer their
// subscription is cancelled at period end (HandOverOnTransfer). The new
// owner can subscribe right away: CreateCheckout sees a live subscription
// that is cancelled and still running and starts a *scheduled* one instead
// of refusing — a zero-amount first payment that only sets up a mandate
// (card / PayPal), and a Mollie subscription whose first charge is on the
// previous subscription's next_charge_at. When that date passes,
// promoteScheduled expires the previous subscription and makes the
// scheduled one active, so the project never drops to Free and nobody pays
// for the same period twice.
//
// The same path also lets an owner who cancelled at period end subscribe
// again before the period ends, without paying twice.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrPaymentOverdue is returned for a project whose subscription is
	// past_due: there is no paid period to hand over, so a transfer has to
	// wait until the owner settles it.
	ErrPaymentOverdue = errors.New("billing: the project's last payment failed")

	// ErrAlreadyScheduled is returned when the project already has a
	// confirmed subscription waiting to start. Handler returns 409.
	ErrAlreadyScheduled = errors.New("billing: a subscription is already scheduled for this project")
)

// minScheduleLead is how far in the future the running period must end for
// a checkout to schedule a follow-on subscription rather than refuse. Close
// to the end, the zero-amount checkout could complete after the period has
// ended; the owner can then subscribe normally once the sweep has run.
const minScheduleLead = time.Hour

// HandoverResult reports what HandOverOnTransfer did.
type HandoverResult struct {
	// HandedOver is true when a paid subscription was cancelled at period
	// end so the new owner can take over from PaidUntil.
	HandedOver bool
	PaidUntil  *time.Time
	// MollieCancelFailed: the local row is cancelled at period end, but
	// Mollie didn't confirm the cancel. The period-end promotion retries
	// it, and a charge Mollie creates after the cancel is refunded
	// (recordRecurringPayment).
	MollieCancelFailed bool
}

// CheckTransferable refuses a transfer while the project's subscription is
// past due.
func (s *Service) CheckTransferable(ctx context.Context, projectID string) error {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM public.subscriptions
		  WHERE project_id = $1 AND status = 'past_due'`,
		projectID,
	).Scan(&n); err != nil {
		return fmt.Errorf("billing: transfer check: %w", err)
	}
	if n > 0 {
		return ErrPaymentOverdue
	}
	return nil
}

// HandOverOnTransfer runs after a project's ownership has moved. It closes
// whatever the previous owner had open on the project and cancels their
// paid subscription at period end.
func (s *Service) HandOverOnTransfer(ctx context.Context, projectID string) (*HandoverResult, error) {
	res := &HandoverResult{}

	// 1. An open checkout of the previous owner. If its payment still
	// completes, activateFromFirstPayment refunds it (the row is no longer
	// 'incomplete' and the payer no longer owns the project).
	if _, err := s.pool.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'canceled', canceled_at = now()
		  WHERE project_id = $1 AND status = 'incomplete'`,
		projectID,
	); err != nil {
		return res, fmt.Errorf("billing: close open checkout: %w", err)
	}

	// 2. A follow-on subscription the previous owner had scheduled.
	rows, err := s.pool.Query(ctx,
		`SELECT id, mollie_subscription_id, mollie_customer_id
		   FROM public.subscriptions
		  WHERE project_id = $1 AND status = 'scheduled'`,
		projectID,
	)
	if err != nil {
		return res, fmt.Errorf("billing: load scheduled subscription: %w", err)
	}
	type sched struct {
		id             string
		mollieSub, cus *string
	}
	var scheduled []sched
	for rows.Next() {
		var sc sched
		if err := rows.Scan(&sc.id, &sc.mollieSub, &sc.cus); err != nil {
			rows.Close()
			return res, fmt.Errorf("billing: scan scheduled subscription: %w", err)
		}
		scheduled = append(scheduled, sc)
	}
	rows.Close()
	for _, sc := range scheduled {
		if sc.mollieSub != nil && sc.cus != nil && *sc.mollieSub != "" {
			if _, cerr := s.client.CancelSubscription(ctx, *sc.cus, *sc.mollieSub); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
				return res, fmt.Errorf("billing: cancel scheduled mollie subscription: %w", cerr)
			}
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'canceled', canceled_at = now()
			  WHERE id = $1 AND status = 'scheduled'`,
			sc.id,
		); err != nil {
			return res, fmt.Errorf("billing: cancel scheduled subscription: %w", err)
		}
	}

	// 3. The paid subscription.
	var (
		subID          string
		mollieSub, cus *string
		nextChargeAt   *time.Time
	)
	err = s.pool.QueryRow(ctx,
		`SELECT id, mollie_subscription_id, mollie_customer_id, next_charge_at
		   FROM public.subscriptions
		  WHERE project_id = $1 AND status IN ('active', 'past_due')
		  ORDER BY created_at DESC LIMIT 1`,
		projectID,
	).Scan(&subID, &mollieSub, &cus, &nextChargeAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("billing: load live subscription: %w", err)
	}

	if mollieSub != nil && cus != nil && *mollieSub != "" && *cus != "" {
		if _, cerr := s.client.CancelSubscription(ctx, *cus, *mollieSub); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
			res.MollieCancelFailed = true
			slog.Error("billing: handover — mollie cancel failed; retried at period end, later charges are refunded",
				"project_id", projectID, "subscription_id", subID, "error", cerr)
		}
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE public.subscriptions
		    SET canceled_at = COALESCE(canceled_at, now())
		  WHERE id = $1`,
		subID,
	); err != nil {
		return res, fmt.Errorf("billing: stamp canceled_at: %w", err)
	}
	res.HandedOver = true
	res.PaidUntil = nextChargeAt
	return res, nil
}

// scheduleTarget returns the live subscription a new one can follow without
// a gap: active, cancelled at period end, and with the period running for
// at least minScheduleLead.
func (s *Service) scheduleTarget(ctx context.Context, projectID string) (predID string, startsAt time.Time, ok bool, err error) {
	var (
		status       string
		canceledAt   *time.Time
		nextChargeAt *time.Time
	)
	err = s.pool.QueryRow(ctx,
		`SELECT id, status, canceled_at, next_charge_at
		   FROM public.subscriptions
		  WHERE project_id = $1 AND status IN ('incomplete', 'active', 'past_due')
		  LIMIT 1`,
		projectID,
	).Scan(&predID, &status, &canceledAt, &nextChargeAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("billing: existing-sub check: %w", err)
	}
	if status == "active" && canceledAt != nil && nextChargeAt != nil &&
		nextChargeAt.After(time.Now().Add(minScheduleLead)) {
		return predID, *nextChargeAt, true, nil
	}
	return predID, time.Time{}, false, ErrAlreadySubscribed
}

// createScheduledCheckout starts the zero-amount mandate checkout for a
// subscription that begins at startsAt. No invoice: nothing is charged
// until Mollie's first recurring payment on startsAt.
func (s *Service) createScheduledCheckout(ctx context.Context, userID, projectID, projectName, planCode string, price int, predID string, startsAt time.Time) (*CheckoutResult, error) {
	// An earlier attempt the user abandoned is replaced; a confirmed one
	// stays (cancel it from the billing page to change it).
	var (
		existingID  string
		existingSub *string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, mollie_subscription_id FROM public.subscriptions
		  WHERE project_id = $1 AND status = 'scheduled'`,
		projectID,
	).Scan(&existingID, &existingSub)
	switch {
	case err == nil && existingSub != nil && *existingSub != "":
		return nil, ErrAlreadyScheduled
	case err == nil:
		if _, uerr := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET status = 'canceled', canceled_at = now()
			  WHERE id = $1 AND status = 'scheduled' AND mollie_subscription_id IS NULL`,
			existingID,
		); uerr != nil {
			return nil, fmt.Errorf("billing: replace abandoned scheduled checkout: %w", uerr)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("billing: scheduled-sub check: %w", err)
	}

	customerID, err := s.ensureMollieCustomer(ctx, userID)
	if err != nil {
		return nil, err
	}

	var subscriptionID string
	err = s.pool.QueryRow(ctx,
		`INSERT INTO public.subscriptions
		    (project_id, mollie_customer_id, plan, price_cents, currency,
		     billing_interval, status, starts_at, predecessor_id)
		 VALUES ($1, $2, $3, $4, 'EUR', '1 month', 'scheduled', $5, $6)
		 RETURNING id`,
		projectID, customerID, planCode, price, startsAt, predID,
	).Scan(&subscriptionID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrAlreadyScheduled
		}
		return nil, fmt.Errorf("billing: insert scheduled subscription: %w", err)
	}

	payment, err := s.client.CreatePayment(ctx, mollie.PaymentCreateRequest{
		Amount:       mollie.AmountFromCents(0, "EUR"),
		Description:  fmt.Sprintf("Eurobase %s — %s (first charge %s)", planCode, projectName, startsAt.UTC().Format("2006-01-02")),
		RedirectURL:  fmt.Sprintf("%s/p/%s/billing?status=success", s.config.ConsoleBaseURL, projectID),
		WebhookURL:   fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		Method:       mollie.MandateMethods,
		CustomerID:   customerID,
		SequenceType: mollie.SequenceTypeFirst,
		Metadata: map[string]string{
			"subscription_id": subscriptionID,
			"project_id":      projectID,
			"plan_code":       planCode,
		},
	}, mollie.WithIdempotencyKey("checkout:"+subscriptionID))
	if err != nil || payment.Links.Checkout == nil || payment.Links.Checkout.Href == "" {
		if _, derr := s.pool.Exec(ctx,
			`DELETE FROM public.subscriptions
			  WHERE id = $1 AND status = 'scheduled' AND mollie_subscription_id IS NULL`,
			subscriptionID,
		); derr != nil {
			slog.Warn("billing: rollback scheduled subscription failed", "subscription_id", subscriptionID, "error", derr)
		}
		if err != nil {
			return nil, fmt.Errorf("billing: create mollie mandate payment: %w", err)
		}
		return nil, fmt.Errorf("billing: mollie returned no checkout URL (payment %s)", payment.ID)
	}

	slog.Info("billing: scheduled checkout created",
		"user_id", userID,
		"project_id", projectID,
		"subscription_id", subscriptionID,
		"mollie_payment_id", payment.ID,
		"starts_at", startsAt,
	)
	st := startsAt
	return &CheckoutResult{
		SubscriptionID: subscriptionID,
		CheckoutURL:    payment.Links.Checkout.Href,
		StartsAt:       &st,
	}, nil
}

// promoteScheduled makes a project's confirmed scheduled subscription
// active once its start date has passed, and expires the cancelled
// subscription it follows. Returns false when there was nothing to do.
// Safe to call concurrently (row locks) and repeatedly.
func promoteScheduled(ctx context.Context, pool *pgxpool.Pool, client *mollie.Client, projectID string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var succID, plan string
	err = tx.QueryRow(ctx,
		`SELECT id, plan FROM public.subscriptions
		  WHERE project_id = $1 AND status = 'scheduled'
		    AND mollie_subscription_id IS NOT NULL
		    AND starts_at <= now()
		  FOR UPDATE`,
		projectID,
	).Scan(&succID, &plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load scheduled subscription: %w", err)
	}

	type pred struct {
		id             string
		canceled       bool
		mollieSub, cus *string
	}
	rows, err := tx.Query(ctx,
		`SELECT id, canceled_at IS NOT NULL, mollie_subscription_id, mollie_customer_id
		   FROM public.subscriptions
		  WHERE project_id = $1 AND status IN ('active', 'past_due')
		  FOR UPDATE`,
		projectID,
	)
	if err != nil {
		return false, fmt.Errorf("load predecessor: %w", err)
	}
	var preds []pred
	for rows.Next() {
		var p pred
		if err := rows.Scan(&p.id, &p.canceled, &p.mollieSub, &p.cus); err != nil {
			rows.Close()
			return false, err
		}
		preds = append(preds, p)
	}
	rows.Close()
	for _, p := range preds {
		if !p.canceled {
			// Never retire a subscription nobody cancelled.
			slog.Error("billing: scheduled subscription not promoted — the live one isn't cancelled",
				"project_id", projectID, "scheduled_id", succID, "live_id", p.id)
			return false, nil
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'expired', canceled_at = COALESCE(canceled_at, now())
		  WHERE project_id = $1 AND status IN ('active', 'past_due')`,
		projectID,
	); err != nil {
		return false, fmt.Errorf("expire predecessor: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'active', started_at = COALESCE(started_at, now())
		  WHERE id = $1`,
		succID,
	); err != nil {
		return false, fmt.Errorf("activate scheduled subscription: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE public.projects
		    SET plan = $1, legacy_pro_grace_until = NULL
		  WHERE id = $2 AND plan NOT IN ('team', 'legal_team')`,
		plan, projectID,
	); err != nil {
		return false, fmt.Errorf("set project plan: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}

	// The handover cancelled these at Mollie already; repeat in case that
	// call failed. Best-effort.
	for _, p := range preds {
		if client != nil && p.mollieSub != nil && p.cus != nil && *p.mollieSub != "" {
			if _, cerr := client.CancelSubscription(ctx, *p.cus, *p.mollieSub); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
				slog.Warn("billing: cancel of expired subscription at mollie failed",
					"project_id", projectID, "subscription_id", p.id, "error", cerr)
			}
		}
	}
	slog.Info("billing: scheduled subscription took over",
		"project_id", projectID, "subscription_id", succID)
	return true, nil
}
