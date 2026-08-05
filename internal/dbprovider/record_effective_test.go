package dbprovider

import (
	"bytes"
	"testing"
)

// The EffectiveCredential method is the single decision point
// between "route SDK traffic as the runtime (non-owner) role — RLS
// enforced" and "fall back to the owner — RLS bypassed". Getting
// the fallback wrong is a cross-tenant data leak (see the loud
// TEAM_TIER_ROUTING warning in gateway/router.go). These tests
// pin the contract.

func TestRecord_EffectiveCredential_FallsBackToOwner(t *testing.T) {
	rec := &Record{
		Username:           "eurobase_owner",
		PasswordCiphertext: []byte("owner-ct"),
		PasswordNonce:      []byte("owner-nonce"),
		PasswordKeyVersion: 3,
		// RuntimeUsername + RuntimePassword* all zero (pre-part-2
		// row from M1/M2 provision).
	}
	user, ct, nonce, ver := rec.EffectiveCredential()
	assertOwner(t, user, ct, nonce, ver)
}

func TestRecord_EffectiveCredential_PrefersRuntime(t *testing.T) {
	runtime := "eurobase_runtime"
	ver16 := int16(5)
	rec := &Record{
		Username:                  "eurobase_owner",
		PasswordCiphertext:        []byte("owner-ct"),
		PasswordNonce:             []byte("owner-nonce"),
		PasswordKeyVersion:        3,
		RuntimeUsername:           &runtime,
		RuntimePasswordCiphertext: []byte("runtime-ct"),
		RuntimePasswordNonce:      []byte("runtime-nonce"),
		RuntimePasswordKeyVersion: &ver16,
	}
	user, ct, nonce, ver := rec.EffectiveCredential()
	if user != "eurobase_runtime" {
		t.Errorf("runtime-preferred user: got %q want eurobase_runtime", user)
	}
	if !bytes.Equal(ct, []byte("runtime-ct")) {
		t.Errorf("runtime-preferred ct: got %q want runtime-ct", ct)
	}
	if !bytes.Equal(nonce, []byte("runtime-nonce")) {
		t.Errorf("runtime-preferred nonce: got %q want runtime-nonce", nonce)
	}
	if ver != 5 {
		t.Errorf("runtime-preferred ver: got %d want 5", ver)
	}
}

// The 000093 CHECK enforces all-or-none at the DB level, so a
// DB-sourced Record will always satisfy one branch or the other.
// But a hand-constructed Record (test fixture, future code) could
// straddle. The prior split-method design would nil-deref on the
// pool-cache hot path in that case. This test pins the fail-safe:
// any half-populated runtime slot degrades to the owner, no panic.
func TestRecord_EffectiveCredential_HalfPopulatedRuntime_FallsBackSafely(t *testing.T) {
	runtime := "eurobase_runtime"
	rec := &Record{
		Username:           "eurobase_owner",
		PasswordCiphertext: []byte("owner-ct"),
		PasswordNonce:      []byte("owner-nonce"),
		PasswordKeyVersion: 3,
		// Only the username is set — the password fields aren't.
		// The old EffectivePasswordSealed would have panicked
		// dereferencing RuntimePasswordKeyVersion.
		RuntimeUsername: &runtime,
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("half-populated Record caused panic: %v", r)
		}
	}()
	user, ct, nonce, ver := rec.EffectiveCredential()
	assertOwner(t, user, ct, nonce, ver)
}

func assertOwner(t *testing.T, user string, ct, nonce []byte, ver int16) {
	t.Helper()
	if user != "eurobase_owner" {
		t.Errorf("owner-fallback user: got %q want eurobase_owner", user)
	}
	if !bytes.Equal(ct, []byte("owner-ct")) {
		t.Errorf("owner-fallback ct: got %q", ct)
	}
	if !bytes.Equal(nonce, []byte("owner-nonce")) {
		t.Errorf("owner-fallback nonce: got %q", nonce)
	}
	if ver != 3 {
		t.Errorf("owner-fallback ver: got %d", ver)
	}
}
