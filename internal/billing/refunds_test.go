package billing

import (
	"testing"
	"time"
)

// TestProratedRefundCents is the money path — every branch of the
// pro-ration calculator is covered so a copy-paste mistake shows
// as a failing test rather than as an under- or over-refund on
// production.
func TestProratedRefundCents(t *testing.T) {
	// Reference period: exactly one month (30 days for the
	// linear math to have clean numbers).
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 30)

	tests := []struct {
		name     string
		amount   int
		start    time.Time
		end      time.Time
		cancel   time.Time
		wantMin  int // inclusive lower bound (ns→cents math has ±1 wobble)
		wantMax  int // inclusive upper bound
	}{
		{
			name:    "cancel-at midpoint refunds ~half",
			amount:  1900,
			start:   start,
			end:     end,
			cancel:  start.AddDate(0, 0, 15),
			wantMin: 949,
			wantMax: 950,
		},
		{
			name:    "cancel-at start refunds full",
			amount:  1900,
			start:   start,
			end:     end,
			cancel:  start,
			wantMin: 1900,
			wantMax: 1900,
		},
		{
			name:    "cancel-at end refunds zero",
			amount:  1900,
			start:   start,
			end:     end,
			cancel:  end,
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:    "cancel past end refunds zero",
			amount:  1900,
			start:   start,
			end:     end,
			cancel:  end.AddDate(0, 0, 5),
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:    "cancel before start refunds full",
			amount:  1900,
			start:   start,
			end:     end,
			cancel:  start.AddDate(0, 0, -1),
			wantMin: 1900,
			wantMax: 1900,
		},
		{
			name:    "zero amount refunds zero",
			amount:  0,
			start:   start,
			end:     end,
			cancel:  start.AddDate(0, 0, 15),
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:    "negative amount refunds zero (defensive)",
			amount:  -500,
			start:   start,
			end:     end,
			cancel:  start.AddDate(0, 0, 15),
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:    "zero-length period refunds zero (defensive divide-guard)",
			amount:  1900,
			start:   start,
			end:     start, // zero-length — Mollie should never send this
			cancel:  start.Add(time.Hour),
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:    "team tier: cancel-at quarter-way refunds ~three-quarters",
			amount:  14900, // €149
			start:   start,
			end:     end,
			cancel:  start.AddDate(0, 0, 8), // ~day 8 of 30
			wantMin: 10920, // 14900 * 22/30 = ~10926 (rounds down)
			wantMax: 10940,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProratedRefundCents(tt.amount, tt.start, tt.end, tt.cancel)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("ProratedRefundCents(%d, ..., cancel=%s) = %d, want [%d, %d]",
					tt.amount, tt.cancel.Format("2006-01-02"), got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// TestFormatRefundDescription is what shows up on the merchant's
// Mollie dashboard. Kept traceable to the invoice so a refund
// dispute is straightforward to walk back.
func TestFormatRefundDescription(t *testing.T) {
	got := FormatRefundDescription("EB-2026-000042")
	want := "Prorated cancel of EB-2026-000042"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
