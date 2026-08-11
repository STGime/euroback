package dbprovider

import (
	"strings"
	"testing"
	"unicode"
)

// Pin the four-class guarantee that Scaleway RDB enforces on
// user create / password rotate. Prod bug that motivated this
// helper: the previous `randomHex(32)` code path failed 400 at
// instance-create with "must contain at least one digit, one
// uppercase, one lowercase and one special character" — no
// project_databases row ever landed → the tenant's Connection
// tab returned 409 forever.
//
// Test runs many iterations because the four-class guarantee is
// baked into the algorithm; even a single failure across 500
// draws is a real regression, not flake.
func TestRandomScalewayPassword_MeetsComplexityPolicy(t *testing.T) {
	const iterations = 500
	for i := 0; i < iterations; i++ {
		pw, err := RandomScalewayPassword(16)
		if err != nil {
			t.Fatalf("iter %d: err %v", i, err)
		}
		if len(pw) < 12 {
			t.Fatalf("iter %d: length %d below Scaleway minimum", i, len(pw))
		}
		if !hasClass(pw, unicode.IsUpper) {
			t.Errorf("iter %d %q: missing uppercase", i, pw)
		}
		if !hasClass(pw, unicode.IsLower) {
			t.Errorf("iter %d %q: missing lowercase", i, pw)
		}
		if !hasClass(pw, unicode.IsDigit) {
			t.Errorf("iter %d %q: missing digit", i, pw)
		}
		if !strings.ContainsAny(pw, scalewayPasswordSpecial) {
			t.Errorf("iter %d %q: missing special from pool %q", i, pw, scalewayPasswordSpecial)
		}
	}
}

// Sub-minimum length request is silently bumped to 12 — Scaleway's
// floor is 8, we want a comfortable margin above so the four
// guaranteed-class positions leave real entropy. The behaviour is
// bump-not-error because the caller is passing a size intent, not
// a strict requirement.
func TestRandomScalewayPassword_LengthFloor(t *testing.T) {
	for _, n := range []int{0, 1, 8, 11} {
		pw, err := RandomScalewayPassword(n)
		if err != nil {
			t.Fatalf("length %d: err %v", n, err)
		}
		if len(pw) != 12 {
			t.Errorf("length %d: got %d, want floor 12", n, len(pw))
		}
	}
}

// URL-safety: a generated password must not carry a character that
// would break DATABASE_URL parsing at any downstream consumer
// (Postgres URI, psql env, bash quoting, k8s secrets). The pool
// deliberately excludes those; test asserts the exclusion holds.
// A future edit that adds ' or / or space to scalewayPasswordSpecial
// would immediately trip this — good, because a broken URL is
// harder to diagnose than a broken password character.
func TestRandomScalewayPassword_URLSafe(t *testing.T) {
	forbidden := "'\"`\\/#? &+$"
	for i := 0; i < 200; i++ {
		pw, err := RandomScalewayPassword(24)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if idx := strings.IndexAny(pw, forbidden); idx >= 0 {
			t.Errorf("iter %d %q: contains DATABASE_URL-hostile char %q at pos %d", i, pw, pw[idx], idx)
		}
	}
}

func hasClass(s string, f func(rune) bool) bool {
	for _, r := range s {
		if f(r) {
			return true
		}
	}
	return false
}
