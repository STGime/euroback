package auth

import (
	"strings"
	"testing"
)

// Closes #59: safePrefix logged 14 chars (8 hex of entropy) into log
// pipelines. Tightened to 12 chars (prefix + 6 hex ≈ 24 bits — enough
// to disambiguate two keys per project, not enough for offline
// brute-force).

func TestSafePrefix_LengthBoundary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"long key truncated to 12+ellipsis", "eb_pk_0123456789abcdef0123", "eb_pk_012345..."},
		{"long secret key same window", "eb_sk_0123456789abcdef0123", "eb_sk_012345..."},
		{"shorter than window returns mask", "eb_pk_short", "***"},
		{"empty returns mask", "", "***"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := safePrefix(tc.in)
			if got != tc.want {
				t.Errorf("safePrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSafePrefix_DoesNotLeakBeyondTwelve(t *testing.T) {
	got := safePrefix("eb_pk_0123456789abcdef0123456789")
	visible := strings.TrimSuffix(got, "...")
	if len(visible) > 12 {
		t.Errorf("safePrefix exposed %d chars; want at most 12", len(visible))
	}
}
