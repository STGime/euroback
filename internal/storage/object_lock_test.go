package storage

import (
	"errors"
	"testing"
	"time"
)

// isObjectLockedError is what the DeleteObject path uses to decide
// whether an S3 error is a retention refusal (translates to HTTP
// 409) vs a real failure (500). Getting this wrong in either
// direction has consequences:
//   - False positive: a real S3 permission error gets translated to
//     "object locked", masking the actual bug.
//   - False negative: a Legal-Team tenant deletes a tax invoice
//     mid-retention and only finds out at the next audit that the
//     GoBD immutability chain is broken.
// Pin the substrings so a future edit doesn't quietly narrow the
// detector.
func TestIsObjectLockedError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"generic AccessDenied", errors.New("AccessDenied: user not authorized"), false},
		{"scaleway object-lock phrasing", errors.New("Access Denied because object protected by object lock"), true},
		{"aws retention-period phrasing", errors.New("access denied by object lock: retention period not expired"), true},
		{"case-insensitive match", errors.New("OPERATION FAILED: OBJECT LOCK RETENTION"), true},
		{"connection reset (real failure)", errors.New("connection reset by peer"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isObjectLockedError(tc.err); got != tc.want {
				t.Errorf("isObjectLockedError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ErrObjectLocked.Error() shape matters because the message flows
// out to slog telemetry (visible to ops). Both paths (with and
// without a known retention-until) must produce a readable line.
func TestErrObjectLockedMessage(t *testing.T) {
	e := &ErrObjectLocked{Bucket: "eurobase-acme", Key: "invoices/2026/inv-001.pdf"}
	if got := e.Error(); got == "" {
		t.Fatalf("empty error message")
	}

	e.RetainUntil = time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := e.Error(); got == "" || !contains(got, "2036") {
		t.Errorf("expected message to include retention year, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
