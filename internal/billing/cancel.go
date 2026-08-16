package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// CancelMode picks the semantics of a user-initiated cancel.
//
//   - CancelModeEndOfPeriod (default): Mollie subscription is
//     canceled, but the local subscription row stays in 'active'
//     until next_charge_at rolls over — the user keeps Pro
//     features they've already paid for. Nothing is refunded.
//   - CancelModeImmediate: Mollie subscription is canceled AND
//     the local row flips to 'canceled' immediately, projects.plan
//     drops to 'free', and a prorated refund is created via
//     Mollie for the unused portion of the current period.
type CancelMode string

const (
	CancelModeEndOfPeriod CancelMode = "end_of_period"
	CancelModeImmediate   CancelMode = "immediate"
)

// CancelResult carries the values the console needs after a
// cancel: which mode fired (so the UI can render the right
// success message), when the plan effectively ends, and how
// much (if anything) was refunded.
type CancelResult struct {
	Mode          CancelMode `json:"mode"`
	EffectiveAt   time.Time  `json:"effective_at"`
	RefundedCents int        `json:"refunded_cents,omitempty"`
}

// CancelSubscription flips a subscription per the requested mode.
// Ownership is enforced against the authenticated user; the
// subscription must be in a live state ('active', 'past_due', or
// 'incomplete') to be cancelable.
//
// End-of-period path:
//   1. Cancel the Mollie subscription (best-effort — ErrNotFound
//      is tolerated as "already gone").
//   2. Local row stays active; canceled_at stamped so PR 5's
//      downgrade sweep will handle the eventual flip to expired
//      at next_charge_at rollover. projects.plan stays 'pro'.
//
// Immediate path:
//   1. Compute prorated refund from the most-recent paid invoice.
//   2. Create the Mollie refund (best-effort — if Mollie is
//      down we still flip local state; the operator can retry
//      the refund manually from the Mollie dashboard).
//   3. Cancel the Mollie subscription.
//   4. Flip subscriptions.status='canceled', canceled_at=now.
//   5. Flip projects.plan='free', reset last_active_at so the
//      30-day pause clock starts fresh (same as PR 5's downgrade
//      sweep — kinder than "you're Free and pause tomorrow").
func (s *Service) CancelSubscription(ctx context.Context, userID, subscriptionID string, mode CancelMode) (*CancelResult, error) {
	if !s.enabled {
		return nil, ErrBillingDisabled
	}
	if mode != CancelModeEndOfPeriod && mode != CancelModeImmediate {
		return nil, fmt.Errorf("%w: unknown cancel mode %q", ErrInvalidPlan, mode)
	}

	// Load + ownership check in one query.
	var (
		projectID              string
		status                 string
		mollieSubID            *string
		mollieCustID           *string
		nextChargeAt           *time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT s.project_id, s.status, s.mollie_subscription_id,
		        s.mollie_customer_id, s.next_charge_at
		   FROM public.subscriptions s
		   JOIN public.projects p ON p.id = s.project_id
		  WHERE s.id = $1 AND p.owner_id = $2::uuid`,
		subscriptionID, userID,
	).Scan(&projectID, &status, &mollieSubID, &mollieCustID, &nextChargeAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load subscription %s: %w", subscriptionID, err)
	}

	// Terminal states can't be re-canceled.
	switch status {
	case "canceled", "expired":
		return nil, ErrSubscriptionAlreadyClosed
	}

	// End-of-period: minimal work, keep the user in Pro until the
	// period naturally rolls over.
	if mode == CancelModeEndOfPeriod {
		if mollieSubID != nil && mollieCustID != nil && *mollieSubID != "" && *mollieCustID != "" {
			if _, cerr := s.client.CancelSubscription(ctx, *mollieCustID, *mollieSubID); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
				slog.Warn("billing: mollie cancel failed (end_of_period)",
					"subscription_id", subscriptionID,
					"mollie_subscription_id", *mollieSubID,
					"error", cerr)
			}
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.subscriptions
			    SET canceled_at = COALESCE(canceled_at, now())
			  WHERE id = $1`,
			subscriptionID,
		); err != nil {
			return nil, fmt.Errorf("stamp canceled_at: %w", err)
		}
		effective := time.Now().UTC()
		if nextChargeAt != nil {
			effective = *nextChargeAt
		}
		slog.Info("billing: subscription canceled at period end",
			"subscription_id", subscriptionID,
			"effective_at", effective,
		)
		return &CancelResult{Mode: CancelModeEndOfPeriod, EffectiveAt: effective}, nil
	}

	// Immediate: compute the prorated refund from the most recent
	// paid invoice on this project. Deliberately look up by
	// project + paid_at DESC (rather than joining subscription_id)
	// so pre-000081 invoices without the FK still resolve.
	var (
		lastInvoiceID     string
		lastInvoiceNumber int64
		lastInvoiceYear   int
		lastAmountCents   int
		lastPaidAt        time.Time
		lastMolliePayment *string
	)
	err = s.pool.QueryRow(ctx,
		`SELECT i.id, i.invoice_number, EXTRACT(YEAR FROM i.created_at)::int,
		        i.amount_cents, i.paid_at, i.mollie_payment_id
		   FROM public.invoices i
		  WHERE i.project_id = $1
		    AND i.status = 'paid'
		    AND i.paid_at IS NOT NULL
		  ORDER BY i.paid_at DESC
		  LIMIT 1`,
		projectID,
	).Scan(&lastInvoiceID, &lastInvoiceNumber, &lastInvoiceYear, &lastAmountCents, &lastPaidAt, &lastMolliePayment)

	// Refund path only runs when we actually have something to
	// refund AND a Mollie payment ID to refund against. A
	// subscription in 'incomplete' state (first payment never
	// went through) has no paid invoice — that's fine, we still
	// flip local state below.
	refundCents := 0
	if err == nil && lastMolliePayment != nil && *lastMolliePayment != "" {
		periodEnd := lastPaidAt.AddDate(0, 1, 0) // one month past paid_at
		if nextChargeAt != nil {
			periodEnd = *nextChargeAt
		}
		refundCents = ProratedRefundCents(lastAmountCents, lastPaidAt, periodEnd, time.Now().UTC())
		if refundCents > 0 {
			_, refErr := s.client.CreateRefund(ctx, *lastMolliePayment, mollie.RefundCreateRequest{
				Amount:      mollie.AmountFromCents(refundCents, "EUR"),
				Description: FormatRefundDescription(formatInvoiceNumber(lastInvoiceYear, lastInvoiceNumber)),
				Metadata: map[string]string{
					"subscription_id": subscriptionID,
					"invoice_id":      lastInvoiceID,
					"reason":          "user_cancel_immediate",
				},
			})
			if refErr != nil {
				// Best-effort: local state MUST still flip so
				// the user stops being billed. Operator retries
				// the refund from Mollie's dashboard.
				slog.Warn("billing: prorated refund failed — local cancel proceeds anyway",
					"subscription_id", subscriptionID,
					"mollie_payment_id", *lastMolliePayment,
					"refund_cents", refundCents,
					"error", refErr)
				refundCents = 0
			}
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("billing: last-invoice lookup failed — proceeding without refund",
			"subscription_id", subscriptionID, "error", err)
	}

	// Cancel Mollie subscription.
	if mollieSubID != nil && mollieCustID != nil && *mollieSubID != "" && *mollieCustID != "" {
		if _, cerr := s.client.CancelSubscription(ctx, *mollieCustID, *mollieSubID); cerr != nil && !errors.Is(cerr, mollie.ErrNotFound) {
			slog.Warn("billing: mollie cancel failed (immediate)",
				"subscription_id", subscriptionID,
				"mollie_subscription_id", *mollieSubID,
				"error", cerr)
		}
	}

	// Flip local state atomically.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE public.subscriptions
		    SET status = 'canceled', canceled_at = COALESCE(canceled_at, now())
		  WHERE id = $1`,
		subscriptionID,
	); err != nil {
		return nil, fmt.Errorf("flip subscription: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE public.projects
		    SET plan = 'free', legacy_pro_grace_until = NULL, last_active_at = now()
		  WHERE id = $1 AND plan = 'pro'`,
		projectID,
	); err != nil {
		return nil, fmt.Errorf("flip project plan: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit immediate cancel: %w", err)
	}

	slog.Info("billing: subscription canceled immediately",
		"subscription_id", subscriptionID,
		"project_id", projectID,
		"refunded_cents", refundCents,
	)
	return &CancelResult{
		Mode:          CancelModeImmediate,
		EffectiveAt:   time.Now().UTC(),
		RefundedCents: refundCents,
	}, nil
}

// ErrSubscriptionNotFound is returned when the (subscription_id,
// owner_id) pair doesn't match. Not-found and not-owned are
// deliberately indistinguishable (info-disclosure guard, matches
// checkout's ErrProjectNotFound reasoning).
var ErrSubscriptionNotFound = errors.New("billing: subscription not found")

// ErrSubscriptionAlreadyClosed is returned when the caller tries
// to cancel a subscription that's already 'canceled' or 'expired'.
// Distinguishes idempotent re-cancel-clicks from real errors.
var ErrSubscriptionAlreadyClosed = errors.New("billing: subscription already closed")

// cancelRequest is the JSON body of POST /platform/billing/subscriptions/:id/cancel.
type cancelRequest struct {
	Mode string `json:"mode"`
}

// HandleCancelSubscription is POST /platform/billing/subscriptions/{id}/cancel.
// Delegates every branch to the service; maps sentinel errors to
// HTTP status codes with the same shape as the checkout handler.
func HandleCancelSubscription(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !svc.Enabled() {
			writeJSONError(w, http.StatusServiceUnavailable, "billing_disabled", "billing is not enabled in this environment")
			return
		}
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		subscriptionID := chi.URLParam(r, "id")
		if subscriptionID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_subscription_id", "subscription id path parameter required")
			return
		}

		// Default to end_of_period if body is missing/empty —
		// less-destructive default, matches how Stripe / Chargebee
		// default their same endpoint.
		req := cancelRequest{Mode: string(CancelModeEndOfPeriod)}
		if r.Body != nil {
			// Ignore decode errors — an empty body is legal.
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		if req.Mode == "" {
			req.Mode = string(CancelModeEndOfPeriod)
		}

		// Policy: prorated-refund immediate cancellation was dropped
		// 2026-08-16 — users always keep Pro through the current
		// billing period. The service still implements the immediate
		// mode (kept for a possible future policy re-enable + dead-
		// code test coverage); guard here so direct API callers
		// can't bypass what the console modal no longer offers.
		if req.Mode == string(CancelModeImmediate) {
			writeJSONError(w, http.StatusBadRequest, "immediate_cancel_disabled",
				"immediate cancellation with prorated refund is not offered — subscription will end at the current period's end")
			return
		}

		res, err := svc.CancelSubscription(r.Context(), claims.Subject, subscriptionID, CancelMode(req.Mode))
		if err != nil {
			switch {
			case errors.Is(err, ErrBillingDisabled):
				writeJSONError(w, http.StatusServiceUnavailable, "billing_disabled", "billing is not enabled")
			case errors.Is(err, ErrSubscriptionNotFound):
				writeJSONError(w, http.StatusNotFound, "subscription_not_found", "subscription not found")
			case errors.Is(err, ErrSubscriptionAlreadyClosed):
				writeJSONError(w, http.StatusConflict, "already_closed", "subscription is already canceled or expired")
			case errors.Is(err, ErrInvalidPlan):
				writeJSONError(w, http.StatusBadRequest, "invalid_mode", err.Error())
			default:
				slog.Error("billing: cancel failed",
					"user_id", claims.Subject,
					"subscription_id", subscriptionID,
					"mode", req.Mode,
					"error", err,
				)
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "cancel failed")
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}
}
