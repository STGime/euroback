package auth

import (
	_ "embed"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Password policy for platform (console) signup, password reset, and
// change-password. NIST SP 800-63B-aligned: enforce a minimum LENGTH
// and screen against a list of common / weak values, rather than
// composition rules (forced upper/lower/digit/symbol), which push users
// toward predictable patterns ("Password1!") without adding real
// entropy. No breach-corpus (HaveIBeenPwned) check — that API is fronted
// by Cloudflare, which the sovereignty rules forbid; the local blocklist
// in common_passwords.txt is the substitute.

const (
	// PlatformPasswordMinLength is the minimum length for a platform
	// password. 12 is the NIST-recommended floor for user-chosen
	// secrets screened against a blocklist.
	PlatformPasswordMinLength = 12
	// PlatformPasswordMaxLength caps input at bcrypt's hard limit.
	// bcrypt silently truncates input beyond 72 BYTES, so a longer
	// password would have its tail ignored — two distinct long
	// passwords sharing a 72-byte prefix would collide. Reject rather
	// than truncate, so the strength the user thinks they chose is the
	// strength that's actually hashed.
	PlatformPasswordMaxLength = 72
)

// ErrWeakPassword is the typed error returned when a password fails the
// strength policy. The message is user-facing and safe to surface.
type ErrWeakPassword struct{ Reason string }

func (e *ErrWeakPassword) Error() string { return e.Reason }

//go:embed common_passwords.txt
var commonPasswordsRaw string

// commonPasswords is the parsed blocklist (lowercased), built once at
// package init from the embedded file.
var commonPasswords = parseCommonPasswords(commonPasswordsRaw)

func parseCommonPasswords(raw string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[strings.ToLower(line)] = struct{}{}
	}
	return out
}

// ValidatePasswordStrength enforces the platform password policy. email
// is the account's email (used to reject passwords derived from it);
// pass "" when no email is in scope (e.g. a standalone strength check).
// Returns *ErrWeakPassword on failure so callers can distinguish a
// policy rejection from an internal error.
func ValidatePasswordStrength(password, email string) error {
	// Length is measured in BYTES for the max (bcrypt's unit) and in
	// runes for the min (so a 12-emoji password isn't rejected for
	// "being too short" in bytes — though it's plenty of entropy).
	if utf8.RuneCountInString(password) < PlatformPasswordMinLength {
		return &ErrWeakPassword{Reason: fmt.Sprintf("password must be at least %d characters", PlatformPasswordMinLength)}
	}
	if len(password) > PlatformPasswordMaxLength {
		return &ErrWeakPassword{Reason: fmt.Sprintf("password must be at most %d bytes", PlatformPasswordMaxLength)}
	}

	lower := strings.ToLower(strings.TrimSpace(password))

	// Exact match against the blocklist.
	if _, bad := commonPasswords[lower]; bad {
		return &ErrWeakPassword{Reason: "that password is too common — choose something less predictable"}
	}
	// Base-word match: strip trailing digits and common trailing
	// symbols, then re-check. Catches "password1234", "welcome2024!".
	if core := stripTrailingFluff(lower); core != lower && len(core) >= 4 {
		if _, bad := commonPasswords[core]; bad {
			return &ErrWeakPassword{Reason: "that password is too common — choose something less predictable"}
		}
	}

	// Trivial structural weakness: a single repeated character, or a
	// straight ascending/descending run (e.g. "aaaaaaaaaaaa",
	// "123456789012", "abcdefghijkl").
	if isSingleRepeatedRune(password) {
		return &ErrWeakPassword{Reason: "password must not be a single repeated character"}
	}
	if isSequential(lower) {
		return &ErrWeakPassword{Reason: "password must not be a simple sequence of characters"}
	}

	// Reject passwords derived from the email. Compare the local part
	// (before '@') and the full address, case-insensitively, in both
	// directions, once the shared token is long enough to be meaningful.
	if email != "" {
		el := strings.ToLower(strings.TrimSpace(email))
		if el != "" && strings.Contains(lower, el) {
			return &ErrWeakPassword{Reason: "password must not contain your email address"}
		}
		if local, _, ok := strings.Cut(el, "@"); ok && len(local) >= 4 && strings.Contains(lower, local) {
			return &ErrWeakPassword{Reason: "password must not contain your email name"}
		}
	}

	return nil
}

// stripTrailingFluff removes trailing digits and a small set of common
// trailing symbols so a padded common word reduces to its base.
func stripTrailingFluff(s string) string {
	return strings.TrimRight(s, "0123456789!@#$%^&*._-")
}

func isSingleRepeatedRune(s string) bool {
	if s == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(s)
	for _, r := range s {
		if r != first {
			return false
		}
	}
	return true
}

// isSequential reports whether s is one straight run of consecutive
// code points, ascending or descending (step of exactly +1 or -1 the
// whole way). Requires the whole string to be one run, so a real
// password that merely contains "123" is unaffected.
func isSequential(s string) bool {
	rs := []rune(s)
	if len(rs) < 4 {
		return false
	}
	asc, desc := true, true
	for i := 1; i < len(rs); i++ {
		d := rs[i] - rs[i-1]
		if d != 1 {
			asc = false
		}
		if d != -1 {
			desc = false
		}
	}
	return asc || desc
}
