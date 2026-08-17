package tenant

import "testing"

// TestSMSGateBlocks pins the transition matrix for the #329 config-save
// gate. The gate blocks the enable-transition ONLY — a churned Pro
// project with `phone.enabled=true` persisted from its Pro days must
// still be able to save unrelated auth-config changes.
//
// This was the 🟡 finding on PR #418: the original gate blocked on
// posted `phone.enabled=true` alone, which silently trapped every
// downgraded Pro project (the console re-posts the stale true on
// every save → 402 on every unrelated field change).
func TestSMSGateBlocks(t *testing.T) {
	cases := []struct {
		name    string
		plan    string
		current bool
		posted  bool
		want    bool
	}{
		// Paid plans: never blocked, regardless of transition.
		{"pro enable", "pro", false, true, false},
		{"pro keep-on", "pro", true, true, false},
		{"pro disable", "pro", true, false, false},
		{"team enable", "team", false, true, false},
		{"legal_team enable", "legal_team", false, true, false},

		// Free (or fail-closed unknown): block the off→on flip only.
		{"free enable — BLOCKED", "free", false, true, true},
		{"free keep-on (churned Pro, stale true) — allowed", "free", true, true, false},
		{"free disable-stale — allowed", "free", true, false, false},
		{"free keep-off — allowed", "free", false, false, false},

		// Unknown plan: fail-closed identically to free.
		{"unknown enable — BLOCKED", "enterprise-2028", false, true, true},
		{"unknown keep-on — allowed (converge on next save)", "enterprise-2028", true, true, false},
		{"empty enable — BLOCKED", "", false, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := smsGateBlocks(tc.plan, tc.current, tc.posted)
			if got != tc.want {
				t.Errorf("smsGateBlocks(plan=%q, current=%v, posted=%v) = %v, want %v",
					tc.plan, tc.current, tc.posted, got, tc.want)
			}
		})
	}
}
