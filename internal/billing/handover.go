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
//
// All database changes happen first, in one transaction, so that once it
// commits every later safety net applies even if Mollie can't be reached:
// a charge Mollie still creates on a subscription with canceled_at set is
// refunded (recordRecurringPayment), and the period-end takeover repeats
// the Mollie cancel. The Mollie calls come after, each best-effort.
func (s *Service) HandOverOnTransfer(ctx context.Context, projectID string) (*HandoverResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return &HandoverResult{}, fmt.Errorf("billing: handover begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, finish, err := s.HandOverInTx(ctx, tx, projectID)
	if err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("billing: handover commit: %w", err)
	}
	res.MollieCancelFailed = !finish(ctx)
	return res, nil
}

// HandOverInTx makes the handover's database changes in the caller's
// transaction — the ownership transfer's own, so the two commit or fail
// together. finish, called after the commit, stops the charges at Mollie;
// it reports false when a Mollie cancel failed (later charges are then
// refunded and the cancel retried at period end).
func (s *Service) HandOverInTx(ctx context.Context, tx pgx.Tx, projectID string) (*HandoverResult, func(context.Context) bool, error) {
	res := &HandoverResult{}
	noop := func(context.Context) bool { return true }

	type mollieSubRef struct{ subID, cus string }
	var toCancel []mollieSubRef

	// 1. An open checkout of the previous owner, and a follow-on
	// subscription they had scheduled. If an open checkout's payment still
	// completes, activateFromFirstPayment refunds it.
	rows, err := tx.Query(ctx,
		`UPDATE public.subscriptions
		    SET status = 'canceled', canceled_at = now()
		  WHERE project_id = $1 AND status IN ('incomplete', 'scheduled')
		  RETURNING mollie_subscription_id, mollie_customer_id`,
		projectID,
	)
	if err != nil {
		return res, noop, fmt.Errorf("billing: close open subscriptions: %w", err)
	}
	for rows.Next() {
		var sub, cus *string
		if err := rows.Scan(&sub, &cus); err != nil {
			rows.Close()
			return res, noop, fmt.Errorf("billing: scan closed subscription: %w", err)
		}
		if sub != nil && cus != nil && *sub != "" && *cus != "" {
			toCancel = append(toCancel, mollieSubRef{*sub, *cus})
		}
	}
	rows.Close()

	// 2. The paid subscription: cancelled at period end.
	var (
		subID          string
		mollieSub, cus *string
		nextChargeAt   *time.Time
	)
	err = tx.QueryRow(ctx,
		`SELECT id, mollie_subscription_id, mollie_customer_id, next_charge_at
		   FROM public.subscriptions
		  WHERE project_id = $1 AND status IN ('active', 'past_due')
		  ORDER BY created_at DESC LIMIT 1
		  FOR UPDATE`,
		projectID,
	).Scan(&subID, &mollieSub, &cus, &nextChargeAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return res, noop, fmt.Errorf("billing: load live subscription: %w", err)
	default:
		// Without a known period end, nothing would ever end this
		// subscription or let the new owner take over: derive it from the
		// last paid charge (one month after it), else end it now.
		if nextChargeAt == nil {
			var lastPaid *time.Time
			if err := tx.QueryRow(ctx,
				`SELECT max(paid_at) FROM public.invoices
				  WHERE subscription_id = $1 AND status = 'paid'`,
				subID,
			).Scan(&lastPaid); err != nil {
				return res, noop, fmt.Errorf("billing: last paid charge: %w", err)
			}
			end := time.Now().UTC()
			if lastPaid != nil {
				if next := addMonthClamped(lastPaid.UTC()); next.After(end) {
					end = next
				}
			}
			nextChargeAt = &end
			slog.Warn("billing: handover — subscription had no next_charge_at; derived one",
				"project_id", projectID, "subscription_id", subID, "next_charge_at", end)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE public.subscriptions
			    SET canceled_at = COALESCE(canceled_at, now()),
			        next_charge_at = $2
			  WHERE id = $1`,
			subID, nextChargeAt,
		); err != nil {
			return res, noop, fmt.Errorf("billing: stamp canceled_at: %w", err)
		}
		if mollieSub != nil && cus != nil && *mollieSub != "" && *cus != "" {
			toCancel = append(toCancel, mollieSubRef{*mollieSub, *cus})
		}
		res.HandedOver = true
		res.PaidUntil = nextChargeAt
	}
	// 3. Stop the charges at Mollie, after the caller commits. Keep going
	// past failures.
	finish := func(ctx context.Context) bool {
		ok := true
		for _, m := range toCancel {
			if _, cerr := s.client.CancelSubscription(ctx, m.cus, m.subID); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
				ok = false
				slog.Error("billing: handover — mollie cancel failed; later charges are refunded and the cancel is retried at period end",
					"project_id", projectID, "mollie_subscription_id", m.subID, "error", cerr)
			}
		}
		return ok
	}
	return res, finish, nil
}

// scheduleTarget returns the live subscription a new one can follow without
// a gap: active, cancelled at period end, and with the period running for
// at least minScheduleLead.
func (s *Service) scheduleTarget(ctx context.Context, projectID string) (predID string, startsAt time.Time, ok bool, err error) {
	// A confirmed subscription already waiting to start: a second one
	// (scheduled or immediate) would charge twice.
	var waiting int
	if err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM public.subscriptions
		  WHERE project_id = $1 AND status = 'scheduled'
		    AND mollie_subscription_id IS NOT NULL`,
		projectID,
	).Scan(&waiting); err != nil {
		return "", time.Time{}, false, fmt.Errorf("billing: scheduled-sub check: %w", err)
	}
	if waiting > 0 {
		return "", time.Time{}, false, ErrAlreadyScheduled
	}

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

	// Inserted only while the caller still owns the project (the
	// ownership check above was before the Mollie customer call).
	var subscriptionID string
	err = s.pool.QueryRow(ctx,
		`INSERT INTO public.subscriptions
		    (project_id, mollie_customer_id, plan, price_cents, currency,
		     billing_interval, status, starts_at, predecessor_id)
		 SELECT $1, $2, $3, $4, 'EUR', '1 month', 'scheduled', $5, $6
		  WHERE EXISTS (SELECT 1 FROM public.projects
		                 WHERE id = $1 AND owner_id = $7::uuid AND status = 'active')
		 RETURNING id`,
		projectID, customerID, planCode, price, startsAt, predID, userID,
	).Scan(&subscriptionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
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
//
// charged: Mollie has charged (or tried to charge) the scheduled
// subscription, so it has started whatever starts_at says — Mollie's
// start date is a day in Amsterdam, starts_at is midnight UTC of that day.
func promoteScheduled(ctx context.Context, pool *pgxpool.Pool, client *mollie.Client, projectID string, charged bool) (bool, error) {
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
		    AND (starts_at <= now() OR $2)
		  FOR UPDATE`,
		projectID, charged,
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
			// Never retire a subscription nobody cancelled — and don't
			// let the scheduled one charge alongside it: cancel it.
			slog.Error("billing: scheduled subscription superseded by an uncancelled live one — cancelling it",
				"project_id", projectID, "scheduled_id", succID, "live_id", p.id)
			var mSub, cus *string
			if err := tx.QueryRow(ctx,
				`UPDATE public.subscriptions
				    SET status = 'canceled', canceled_at = now()
				  WHERE id = $1
				  RETURNING mollie_subscription_id, mollie_customer_id`,
				succID,
			).Scan(&mSub, &cus); err != nil {
				return false, fmt.Errorf("cancel superseded scheduled subscription: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return false, err
			}
			if client != nil && mSub != nil && cus != nil && *mSub != "" {
				if _, cerr := client.CancelSubscription(ctx, *cus, *mSub); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
					slog.Error("billing: cancel of superseded scheduled subscription at mollie failed",
						"project_id", projectID, "subscription_id", succID, "error", cerr)
				}
			}
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
