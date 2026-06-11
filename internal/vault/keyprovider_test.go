package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
)

func testMaster() []byte {
	m := make([]byte, 32)
	for i := range m {
		m[i] = byte(i)
	}
	return m
}

// Version 0 must return the master key verbatim so rows sealed before
// per-tenant derivation existed keep decrypting unchanged.
func TestHKDFProvider_LegacyVersionReturnsMaster(t *testing.T) {
	p := newHKDFKeyProvider(testMaster())
	got, err := p.DeriveKey(context.Background(), "tenant_abc", legacyKeyVersion)
	if err != nil {
		t.Fatalf("DeriveKey v0: %v", err)
	}
	if !bytes.Equal(got, testMaster()) {
		t.Errorf("v0 key = %x, want master %x", got, testMaster())
	}
}

// Derivation must be deterministic: same (tenant, version) -> same 32-byte key.
func TestHKDFProvider_Deterministic(t *testing.T) {
	p := newHKDFKeyProvider(testMaster())
	ctx := context.Background()
	k1, err := p.DeriveKey(ctx, "tenant_abc", 1)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	k2, err := p.DeriveKey(ctx, "tenant_abc", 1)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if !bytes.Equal(k1, k2) {
		t.Errorf("derivation not deterministic: %x != %x", k1, k2)
	}
	if len(k1) != 32 {
		t.Errorf("derived key length = %d, want 32", len(k1))
	}
}

// Different tenants must get different keys (per-tenant cryptographic
// separation), and a derived key must never equal the master.
func TestHKDFProvider_PerTenantSeparation(t *testing.T) {
	p := newHKDFKeyProvider(testMaster())
	ctx := context.Background()
	a, _ := p.DeriveKey(ctx, "tenant_a", 1)
	b, _ := p.DeriveKey(ctx, "tenant_b", 1)
	if bytes.Equal(a, b) {
		t.Error("two tenants derived the same key")
	}
	if bytes.Equal(a, testMaster()) {
		t.Error("derived per-tenant key equals the master key")
	}
}

// Bumping the version (rotation) must yield an independent key for the same
// tenant.
func TestHKDFProvider_VersionRotation(t *testing.T) {
	p := newHKDFKeyProvider(testMaster())
	ctx := context.Background()
	v1, _ := p.DeriveKey(ctx, "tenant_a", 1)
	v2, _ := p.DeriveKey(ctx, "tenant_a", 2)
	if bytes.Equal(v1, v2) {
		t.Error("v1 and v2 keys are identical; rotation would be a no-op")
	}
}

func TestHKDFProvider_CurrentVersionIsDerived(t *testing.T) {
	p := newHKDFKeyProvider(testMaster())
	if p.CurrentVersion() != firstDerivedKeyVersion {
		t.Errorf("CurrentVersion = %d, want %d (HKDF, not legacy)", p.CurrentVersion(), firstDerivedKeyVersion)
	}
	if p.CurrentVersion() == legacyKeyVersion {
		t.Error("new writes must not use the legacy master-key version")
	}
}

func TestHKDFProvider_EmptyTenantRejected(t *testing.T) {
	p := newHKDFKeyProvider(testMaster())
	if _, err := p.DeriveKey(context.Background(), "", 1); err == nil {
		t.Error("expected error deriving a non-legacy key with empty tenant")
	}
}

// End-to-end at the crypto layer: a value sealed under a derived key opens
// back to the same plaintext, and a different tenant's key fails to open it.
func TestSealOpen_RoundTripAndIsolation(t *testing.T) {
	p := newHKDFKeyProvider(testMaster())
	ctx := context.Background()
	keyA, _ := p.DeriveKey(ctx, "tenant_a", 1)
	keyB, _ := p.DeriveKey(ctx, "tenant_b", 1)

	ct, nonce, err := encryptWith(keyA, "hunter2")
	if err != nil {
		t.Fatalf("encryptWith: %v", err)
	}

	got, err := decryptWith(keyA, ct, nonce)
	if err != nil {
		t.Fatalf("decryptWith (same key): %v", err)
	}
	if got != "hunter2" {
		t.Errorf("round-trip = %q, want %q", got, "hunter2")
	}

	if _, err := decryptWith(keyB, ct, nonce); err == nil {
		t.Error("tenant B's key decrypted tenant A's ciphertext — separation broken")
	}
}

// Cross-language vector pinned in BOTH this Go suite and the Deno runner
// suite (functions-runner/vault_test.ts, issue #201). The runner
// re-implements this derivation with WebCrypto HKDF; if either side
// drifts, its CI job fails. Do not change the vector without changing
// both files.
func TestHKDFProvider_CrossLanguageVector(t *testing.T) {
	p := newHKDFKeyProvider(testMaster())
	key, err := p.DeriveKey(context.Background(), "tenant_11111111_2222_3333_4444_555555555555", 1)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	want, _ := base64.StdEncoding.DecodeString("RF884Rlm9sIYlm8ig2EQzav21+5drtmIg+vEujfkG54=")
	if !bytes.Equal(key, want) {
		t.Errorf("derived key = %s, want %s — runner vault.ts pins this exact vector",
			base64.StdEncoding.EncodeToString(key), base64.StdEncoding.EncodeToString(want))
	}

	// And the sealed fixture the Deno test decrypts: same key, fixed nonce.
	nonce := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	sealed, _ := base64.StdEncoding.DecodeString("OYT3J2/GBpxo9TmQF9/BYzfodt28C63W91q7+g19CVRVFcSXTA==")
	got, err := decryptWith(key, sealed, nonce)
	if err != nil {
		t.Fatalf("decryptWith fixture: %v", err)
	}
	if got != "mistral_api_key_value" {
		t.Errorf("fixture plaintext = %q, want %q", got, "mistral_api_key_value")
	}
}
