package billing

import (
	"fmt"
	"time"
)

// ProratedRefundCents returns the unused portion of a paid
// subscription period as integer eurocents. Callers pass the last
// paid invoice amount + the billing period bounds + when the
// cancel happened.
//
// Rules:
//
//   - If cancel-at is before period-start (invoice hasn't started
//     yet), the full amount refunds.
//   - If cancel-at is after period-end (period already elapsed),
//     zero refund.
//   - Otherwise: linear proration on wall-clock time. amountCents
//     × (period_end - cancel_at) / (period_end - period_start),
//     rounded DOWN. Rounding down deliberately favours the
//     merchant (Eurobase) on fractional cents; the largest
//     rounding error is €0.01 per cancel, acceptable trade-off
//     for not having to reason about half-cent handling under
//     concurrent cancels.
//
// The function is pure — no I/O, no DB. Wired into
// service.CancelSubscription which stitches the DB lookups.
func ProratedRefundCents(amountCents int, periodStart, periodEnd, cancelAt time.Time) int {
	if amountCents <= 0 {
		return 0
	}
	if !cancelAt.After(periodStart) {
		// Refund everything — the period hasn't started, or
		// starts exactly at the cancel moment.
		return amountCents
	}
	if !cancelAt.Before(periodEnd) {
		// Refund nothing — period is already fully consumed.
		return 0
	}
	total := periodEnd.Sub(periodStart)
	unused := periodEnd.Sub(cancelAt)
	if total <= 0 {
		// Defensive: zero-length or negative-length period.
		// Neither is a legal Mollie state; refund nothing
		// rather than divide-by-zero-panic. Callers see the
		// safe fallback + slog.Error at the call site.
		return 0
	}
	// Use seconds, not nanoseconds. Team tier (14900 cents) times
	// 30-day-worth of nanoseconds (2.6e15) overflows int64
	// (max 9.2e18) when the amount ×'s the duration first. Seconds
	// gives us 6-orders-of-magnitude headroom — enough for any
	// invoice + period we could realistically see.
	unusedSec := int64(unused / time.Second)
	totalSec := int64(total / time.Second)
	if totalSec <= 0 {
		return 0
	}
	refunded := int64(amountCents) * unusedSec / totalSec
	if refunded < 0 {
		return 0
	}
	if refunded > int64(amountCents) {
		return amountCents
	}
	return int(refunded)
}

// FormatRefundDescription renders the Mollie-facing description
// on a refund. Shown on the merchant's Mollie dashboard, so keep
// it short + traceable to the subscription that generated it.
func FormatRefundDescription(invoiceNumber string) string {
	return fmt.Sprintf("Prorated cancel of %s", invoiceNumber)
}
