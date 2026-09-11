package mollie

import (
	"testing"
	"time"
)

// TestDefaultRecurringStartDate_MonthEndClamp pins the month-end
// rollover fix. Go's `AddDate(0, 1, 0)` normalizes impossible
// dates FORWARD (Jan 31 + 1mo = Mar 3, Aug 31 + 1mo = Oct 1).
// That silently shifts the customer's billing anniversary and
// would produce a subtle bug once the calendar hits one of
// these dates. Assert the helper clamps back to the last day of
// the intended month instead.
func TestDefaultRecurringStartDate_MonthEndClamp(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("amsterdam tz not loadable: %v", err)
	}

	cases := []struct {
		in   time.Time
		want string
	}{
		// Jan 31 + 1mo → Feb 28 (non-leap year 2027).
		{time.Date(2027, 1, 31, 12, 0, 0, 0, amsterdam), "2027-02-28"},
		// Jan 31 + 1mo → Feb 29 (leap year 2028).
		{time.Date(2028, 1, 31, 12, 0, 0, 0, amsterdam), "2028-02-29"},
		// Mar 31 + 1mo → Apr 30.
		{time.Date(2026, 3, 31, 12, 0, 0, 0, amsterdam), "2026-04-30"},
		// Aug 31 + 1mo → Sep 30.
		{time.Date(2026, 8, 31, 12, 0, 0, 0, amsterdam), "2026-09-30"},
		// Ordinary date, no clamp needed.
		{time.Date(2026, 9, 10, 14, 30, 0, 0, amsterdam), "2026-10-10"},
		// Feb 28 (non-leap) + 1mo → Mar 28 (no clamp; Mar has that day).
		{time.Date(2027, 2, 28, 12, 0, 0, 0, amsterdam), "2027-03-28"},
	}
	for _, tc := range cases {
		got := DefaultRecurringStartDate(tc.in)
		if got != tc.want {
			t.Errorf("DefaultRecurringStartDate(%s) = %q; want %q",
				tc.in.Format("2006-01-02"), got, tc.want)
		}
	}
}

// TestDefaultRecurringStartDate_TimezoneAwareness verifies that
// the helper computes against Amsterdam local time (Mollie's
// timezone assumption) rather than UTC. Near-midnight UTC on
// the last day of a month is the case that would break if we
// naively used UTC.
func TestDefaultRecurringStartDate_TimezoneAwareness(t *testing.T) {
	// 23:30 UTC on 2026-09-30 is already 01:30 on 2026-10-01 in
	// Amsterdam (CEST, UTC+2 in summer). So the intended-month
	// walkforward should target Nov, and the result should be
	// "2026-11-01".
	utcNight := time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC)
	got := DefaultRecurringStartDate(utcNight)
	if got != "2026-11-01" {
		t.Errorf("Amsterdam-local of %s should walk into Oct → Nov 1; got %q",
			utcNight.Format(time.RFC3339), got)
	}
}

// TestDefaultRecurringStartDate_NeverEmpty is the safety net —
// even in a distroless image without tzdata, the helper must
// return a valid yyyy-mm-dd string so the SubscriptionCreateRequest
// carries something and Mollie doesn't fall back to "today".
func TestDefaultRecurringStartDate_NeverEmpty(t *testing.T) {
	// Regular signup at noon UTC — should always produce a
	// non-empty string one month ahead.
	got := DefaultRecurringStartDate(time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC))
	if got == "" {
		t.Fatal("start date is empty; Mollie will default to today and double-bill")
	}
	if _, err := time.Parse("2006-01-02", got); err != nil {
		t.Fatalf("start date %q is not yyyy-mm-dd: %v", got, err)
	}
}
