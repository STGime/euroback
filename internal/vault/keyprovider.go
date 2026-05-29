package vault

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
	"strconv"
)

// KeyProvider supplies the AES-256 key used to seal/open a tenant's vault
// secrets. It exists so the encryption key can be made per-tenant and
// rotated without re-encrypting historic rows: every ciphertext stores the
// key_version it was sealed with, and decryption asks the provider for the
// key at that version.
//
// The interface is deliberately pluggable. Today the only implementation is
// hkdfKeyProvider (app-layer per-tenant derivation from a master secret).
// Future implementations — a Scaleway Key Manager envelope provider, or a
// customer Hold-Your-Own-Key provider backed by a tenant-supplied key — can
// be dropped in without touching call sites or migrating existing data, as
// long as they own a distinct, non-overlapping version range.
//
// See docs/compliance/encryption-keys.md.
type KeyProvider interface {
	// DeriveKey returns the 32-byte AES-256 key for a tenant at a given
	// version. `tenant` is a stable per-tenant identifier (the tenant
	// schema name); it must be the same value on seal and open.
	DeriveKey(ctx context.Context, tenant string, version int16) ([]byte, error)

	// CurrentVersion is the key version new writes should be sealed with.
	CurrentVersion() int16
}

// legacyKeyVersion (0) means "the row predates per-tenant keys and was sealed
// with the shared master key verbatim". Rows created before migration
// 000057 default to this version, so they keep decrypting unchanged.
const legacyKeyVersion int16 = 0

// hkdfKeyProvider derives a distinct key per (tenant, version) from a single
// master secret using HKDF-SHA256. No external service, no new
// infrastructure — the per-tenant separation and rotation are achieved
// entirely in-process.
//
// This is NOT "platform-cannot-decrypt" BYOK: the platform still holds the
// master secret and can derive any tenant key. It satisfies "per-tenant
// cryptographic separation" and "key rotation"; true BYOK/HYOK requires a
// different KeyProvider (tracked for enterprise).
type hkdfKeyProvider struct {
	master  []byte // 32-byte master secret (VAULT_ENCRYPTION_KEY, decoded)
	current int16  // version used for new writes (>= 1)
}

// firstDerivedKeyVersion (1) is the first HKDF-derived, per-tenant version.
const firstDerivedKeyVersion int16 = 1

func newHKDFKeyProvider(master []byte) *hkdfKeyProvider {
	return &hkdfKeyProvider{master: master, current: firstDerivedKeyVersion}
}

func (p *hkdfKeyProvider) CurrentVersion() int16 { return p.current }

func (p *hkdfKeyProvider) DeriveKey(_ context.Context, tenant string, version int16) ([]byte, error) {
	if version == legacyKeyVersion {
		// Back-compat: rows sealed with the shared master key before
		// per-tenant derivation existed.
		return p.master, nil
	}
	if tenant == "" {
		return nil, fmt.Errorf("vault: empty tenant identifier for key derivation")
	}
	// salt = tenant identifier (per-tenant separation), info = versioned
	// label (rotation: bumping the version yields an independent key).
	info := "eurobase-vault-v" + strconv.Itoa(int(version))
	key, err := hkdf.Key(sha256.New, p.master, []byte(tenant), info, 32)
	if err != nil {
		return nil, fmt.Errorf("vault: derive tenant key: %w", err)
	}
	return key, nil
}
