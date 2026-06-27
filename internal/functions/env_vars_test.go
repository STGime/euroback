package functions

import (
	"context"
	"testing"

	"github.com/eurobase/euroback/internal/vault"
)

// TestResolveEnvVars_Fallback exercises the read-path fallback rules
// documented in migration 000067 — without any DB. The CHECK constraint
// guarantees the (blob, nonce, version) trio is all-or-nothing on disk,
// so the only thing resolveEnvVars has to get right is the dispatch:
//
//  1. blob present  → decrypt (returns error if vault is nil — never silently
//     swap a sealed value for {}, that would mask a misconfigured pod)
//  2. blob absent + legacy present → legacy (the lazy-migration window)
//  3. both absent → empty map (not nil — the runner panics on nil env)
func TestResolveEnvVars_Fallback(t *testing.T) {
	t.Run("both absent → empty map", func(t *testing.T) {
		s := &Service{vault: nil}
		got, err := s.resolveEnvVars(context.Background(), "p", nil, nil, 0, nil)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("want empty map, got %#v", got)
		}
	})

	t.Run("legacy plaintext is returned as-is when no sealed blob", func(t *testing.T) {
		s := &Service{vault: nil}
		legacy := map[string]string{"K": "v"}
		got, err := s.resolveEnvVars(context.Background(), "p", nil, nil, 0, legacy)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got["K"] != "v" {
			t.Fatalf("legacy roundtrip failed: %#v", got)
		}
	})

	t.Run("sealed blob with nil vault → loud error (not silent empty)", func(t *testing.T) {
		s := &Service{vault: nil}
		_, err := s.resolveEnvVars(context.Background(), "p", []byte{0x01}, []byte{0x02}, 1, nil)
		if err == nil {
			t.Fatal("want error when sealed blob present but vault not configured")
		}
	})
}

// TestSealEnvVars_VaultDisabled_FallsThrough confirms that with no vault
// configured (dev mode without VAULT_ENCRYPTION_KEY), sealEnvVars returns
// all-nil so the caller falls through to writing the legacy plaintext
// column. The warning side-effect is checked manually; here we just
// assert the dispatch.
func TestSealEnvVars_VaultDisabled_FallsThrough(t *testing.T) {
	s := &Service{vault: nil}
	blob, nonce, version, err := s.sealEnvVars(context.Background(), "p", map[string]string{"K": "v"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if blob != nil || nonce != nil || version != 0 {
		t.Fatalf("want all-nil seal when vault is nil, got blob=%v nonce=%v version=%d", blob, nonce, version)
	}
}

// TestSealEnvVars_EmptyMap_NoWrite confirms an empty env_vars map never
// produces a sealed trio. This matters for the CHECK constraint: writing
// an empty blob (zero-length BYTEA) is still NOT NULL and would trip the
// "all three populated or all three NULL" guard if version was zero.
func TestSealEnvVars_EmptyMap_NoWrite(t *testing.T) {
	// A configured-but-real vault is fine here; the early-return on
	// len == 0 happens before any crypto/DB call.
	v := mustNewVaultService(t)
	s := &Service{vault: v}
	blob, nonce, version, err := s.sealEnvVars(context.Background(), "p", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if blob != nil || nonce != nil || version != 0 {
		t.Fatalf("want all-nil for empty env_vars, got blob=%v nonce=%v version=%d", blob, nonce, version)
	}
}

// TestNullIfZero is the small adapter that lets an empty Update write SQL
// NULL into env_vars_key_version (so the all-or-nothing CHECK constraint
// holds when nothing is sealed). Easy to invariant.
func TestNullIfZero(t *testing.T) {
	if nullIfZero(0) != nil {
		t.Fatal("0 should map to SQL NULL (nil)")
	}
	if got := nullIfZero(3); got != int16(3) {
		t.Fatalf("nonzero should pass through unchanged, got %#v", got)
	}
}

// TestDerefInt16 pins the read-side counterpart to nullIfZero. The Get
// queries scan env_vars_key_version into *int16 because the column is
// nullable (intentionally, for the legacy + empty-env paths). The
// previous code used a plain `int16` dest and crashed on every function
// without sealed env_vars with `cannot scan NULL into *int16` — which
// the old handler then masked as "function not found" (fixed in #242).
func TestDerefInt16(t *testing.T) {
	if derefInt16(nil) != 0 {
		t.Fatal("nil should map to 0 (matches nullIfZero(0))")
	}
	v := int16(3)
	if got := derefInt16(&v); got != 3 {
		t.Fatalf("non-nil should pass through, got %d", got)
	}
}

// mustNewVaultService constructs a VaultService with a throwaway 32-byte
// master, enough to exercise the seal/open contract end-to-end inside a
// test that doesn't touch the DB.
func mustNewVaultService(t *testing.T) *vault.VaultService {
	t.Helper()
	// A 32-byte zero key, base64-encoded — fine for tests, the
	// keyprovider only checks length.
	const b64ZeroKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	v, err := vault.NewVaultService(nil, b64ZeroKey)
	if err != nil {
		t.Fatalf("vault.NewVaultService: %v", err)
	}
	return v
}

