package dbprovider

import (
	"bytes"
	"testing"
)

// The Effective* methods are the single decision point between
// "route SDK traffic as the runtime (non-owner) role — RLS enforced"
// and "fall back to the owner — RLS bypassed". Getting the fallback
// wrong is a cross-tenant data leak (see the loud TEAM_TIER_ROUTING
// warning in gateway/router.go). These tests pin the contract.

func TestRecord_EffectiveUsername_FallsBackToOwner(t *testing.T) {
	rec := &Record{
		Username:        "eurobase_owner",
		RuntimeUsername: nil, // pre-part-2 row (M1/M2 provision)
	}
	if got := rec.EffectiveUsername(); got != "eurobase_owner" {
		t.Fatalf("EffectiveUsername (runtime nil): got %q want %q", got, "eurobase_owner")
	}
}

func TestRecord_EffectiveUsername_PrefersRuntime(t *testing.T) {
	runtime := "eurobase_runtime"
	rec := &Record{
		Username:        "eurobase_owner",
		RuntimeUsername: &runtime,
	}
	if got := rec.EffectiveUsername(); got != "eurobase_runtime" {
		t.Fatalf("EffectiveUsername (runtime set): got %q want %q", got, "eurobase_runtime")
	}
}

func TestRecord_EffectivePasswordSealed_FallsBackToOwner(t *testing.T) {
	rec := &Record{
		PasswordCiphertext: []byte("owner-ct"),
		PasswordNonce:      []byte("owner-nonce"),
		PasswordKeyVersion: 3,
	}
	ct, nonce, ver := rec.EffectivePasswordSealed()
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

func TestRecord_EffectivePasswordSealed_PrefersRuntimeWhenSet(t *testing.T) {
	runtime := "eurobase_runtime"
	ver16 := int16(5)
	rec := &Record{
		PasswordCiphertext:        []byte("owner-ct"),
		PasswordNonce:             []byte("owner-nonce"),
		PasswordKeyVersion:        3,
		RuntimeUsername:           &runtime,
		RuntimePasswordCiphertext: []byte("runtime-ct"),
		RuntimePasswordNonce:      []byte("runtime-nonce"),
		RuntimePasswordKeyVersion: &ver16,
	}
	ct, nonce, ver := rec.EffectivePasswordSealed()
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
