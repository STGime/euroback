package compliance

import (
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/storage"
)

// Legal-Team M2b (#330) — resolveLongestPrefix is the load-bearing
// piece of the storage-WORM path: every upload derives its S3 Object
// Lock retention from whichever policy wins here. A bug that picked
// the shorter prefix would silently apply the wrong (or no) retention
// to invoices/tax/mandant files and break the §257 HGB / §147 AO
// obligation. Pure function → cheap to pin comprehensively.
func TestResolveLongestPrefix(t *testing.T) {
	// Fixed instant so RetainUntil is deterministic in comparisons.
	// The resolver uses time.Now() internally; we only assert the
	// selected policy (Mode + retention_years derivation), not the
	// exact wall-clock instant.
	cases := []struct {
		name      string
		policies  []StorageRetentionPolicy
		key       string
		wantMode  storage.RetentionMode
		wantYears int // 0 = no retention (zero-value Retention)
	}{
		{
			name:      "no policies → no retention",
			policies:  nil,
			key:       "invoices/2026/01/inv-001.pdf",
			wantMode:  "",
			wantYears: 0,
		},
		{
			name: "no matching prefix → no retention",
			policies: []StorageRetentionPolicy{
				{Prefix: "invoices/", Mode: "compliance", RetentionYears: 10},
			},
			key:       "cat-pictures/fluffy.jpg",
			wantMode:  "",
			wantYears: 0,
		},
		{
			name: "single matching prefix",
			policies: []StorageRetentionPolicy{
				{Prefix: "invoices/", Mode: "compliance", RetentionYears: 10},
			},
			key:       "invoices/2026/01/inv-001.pdf",
			wantMode:  storage.RetentionCompliance,
			wantYears: 10,
		},
		{
			name: "longest wins over root",
			policies: []StorageRetentionPolicy{
				{Prefix: "", Mode: "governance", RetentionYears: 2},
				{Prefix: "invoices/", Mode: "compliance", RetentionYears: 10},
			},
			key:       "invoices/2026/01/inv-001.pdf",
			wantMode:  storage.RetentionCompliance,
			wantYears: 10,
		},
		{
			name: "longest wins over shorter branch",
			policies: []StorageRetentionPolicy{
				{Prefix: "mandant/", Mode: "compliance", RetentionYears: 6},
				{Prefix: "mandant/2026/", Mode: "compliance", RetentionYears: 30},
			},
			key:       "mandant/2026/case-123.pdf",
			wantMode:  storage.RetentionCompliance,
			wantYears: 30,
		},
		{
			name: "root fallback applies when no branch matches",
			policies: []StorageRetentionPolicy{
				{Prefix: "", Mode: "governance", RetentionYears: 2},
				{Prefix: "invoices/", Mode: "compliance", RetentionYears: 10},
			},
			key:       "cat-pictures/fluffy.jpg",
			wantMode:  storage.RetentionGovernance,
			wantYears: 2,
		},
		{
			name: "mode is upper-cased for S3 SDK",
			policies: []StorageRetentionPolicy{
				{Prefix: "tax/", Mode: "compliance", RetentionYears: 10},
			},
			key:       "tax/2026/return.pdf",
			wantMode:  storage.RetentionCompliance, // "COMPLIANCE" — DB stores lower-case
			wantYears: 10,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLongestPrefix(tc.policies, tc.key)
			if got.Mode != tc.wantMode {
				t.Errorf("mode: got %q, want %q", got.Mode, tc.wantMode)
			}
			if tc.wantYears == 0 {
				if !got.RetainUntil.IsZero() {
					t.Errorf("expected zero RetainUntil for no-retention case, got %v", got.RetainUntil)
				}
				return
			}
			// RetainUntil should be roughly now + wantYears years.
			// Allow a ±1 minute skew for test execution time.
			expected := time.Now().UTC().AddDate(tc.wantYears, 0, 0)
			skew := got.RetainUntil.Sub(expected)
			if skew < -time.Minute || skew > time.Minute {
				t.Errorf("RetainUntil off by %v (got %v, expected ~%v)", skew, got.RetainUntil, expected)
			}
		})
	}
}
