package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePasswordStrength_Rejects(t *testing.T) {
	cases := []struct {
		name, password, email string
	}{
		{"too short", "short1!aA", ""},
		{"exactly 11 chars", "abcdEFGH123", ""}, // 11 < 12
		{"common exact", "password", ""},
		{"common padded digits", "password1234", ""},
		{"common padded symbol", "welcome2024!", ""},
		{"qwerty padded", "qwerty123456", ""},
		{"single repeated char", "aaaaaaaaaaaa", ""},
		{"ascending sequence", "123456789012", ""},
		{"alpha sequence", "abcdefghijkl", ""},
		{"contains full email", "xxthisisme@corp.com", "thisisme@corp.com"},
		{"contains email local part", "mylocalpart-rocks!", "localpart@corp.com"},
		{"over 72 bytes", strings.Repeat("aB3$xY7z", 10), ""}, // 80 bytes
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordStrength(tc.password, tc.email)
			if err == nil {
				t.Fatalf("expected rejection for %q", tc.password)
			}
			var weak *ErrWeakPassword
			if !errors.As(err, &weak) {
				t.Fatalf("expected *ErrWeakPassword, got %T: %v", err, err)
			}
		})
	}
}

func TestValidatePasswordStrength_Accepts(t *testing.T) {
	good := []struct{ password, email string }{
		{"correct-horse-battery-staple", "alice@corp.com"},
		{"Tr0ub4dour-and-more", "bob@corp.com"},
		{"a-fairly-long-unique-phrase-2026", "carol@corp.com"},
		{"purple-monkey-dishwasher", ""},
		// contains "123" but is not itself a straight sequence, and
		// local part "ab" is too short (< 4) to trigger the email rule.
		{"north-star-123-lantern", "ab@corp.com"},
	}
	for _, g := range good {
		if err := ValidatePasswordStrength(g.password, g.email); err != nil {
			t.Errorf("expected %q to be accepted, got: %v", g.password, err)
		}
	}
}

func TestValidatePasswordStrength_LengthBoundary(t *testing.T) {
	// Exactly 12 non-trivial characters is accepted.
	if err := ValidatePasswordStrength("gx7-Qm2-Lp9z", ""); err != nil {
		t.Errorf("12-char password should pass: %v", err)
	}
	// 72 bytes exactly is the max (accepted); 73 rejected.
	at72 := strings.Repeat("a1B2c3D4", 9) // 72 bytes, not a common/sequence
	if err := ValidatePasswordStrength(at72, ""); err != nil {
		t.Errorf("72-byte password should pass: %v", err)
	}
	if err := ValidatePasswordStrength(at72+"x", ""); err == nil {
		t.Error("73-byte password should be rejected")
	}
}
