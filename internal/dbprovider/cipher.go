package dbprovider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// Cipher seals and opens the DB passwords stored inline on
// project_databases (owner, runtime and readonly: ciphertext + nonce +
// key_version columns).
//
// The master key is VAULT_ENCRYPTION_KEY (32 bytes, base64). Versions:
//
//   - 1 (legacy, open only): AES-256-GCM under the raw master key, no
//     associated data. The raw key also opens legacy key_version 0 vault
//     rows, so anyone who can make the platform decrypt such a row
//     (e.g. one planted in a Team customer's own database) could have
//     opened these ciphertexts too.
//   - 2 (current): AES-256-GCM under a key HKDF-derived from the master
//     key for this purpose only (domain-separated from the vault's
//     per-tenant keys), with associated data binding the ciphertext to
//     this use. Nothing else seals with that key.
//
// Rollout in two steps, so no running (or rolled-back) component ever
// meets a version it can't open: first every component learns to open
// version 2 while new seals stay version 1 (callers pass
// CipherVersionLegacy); once that is deployed, callers switch to
// CipherVersion and existing rows are re-sealed.
type Cipher struct {
	raw     []byte // version 1 (legacy)
	derived []byte // version 2
	version int16
}

const (
	// CipherVersionLegacy is the raw-master-key version (open only).
	CipherVersionLegacy int16 = 1
	// CipherVersion is the domain-separated version (see the rollout note).
	CipherVersion int16 = 2

	cipherHKDFSalt = "eurobase/project_databases"
	cipherHKDFInfo = "eurobase-dbprovider-v2"
	cipherAAD      = "eurobase/project_databases/password/v2"
)

// NewCipher builds a Cipher from a base64-encoded 32-byte master key;
// version is the version new seals use (CipherVersion in production).
// Mirrors vault.NewVaultService's key-validation shape so ops don't have
// two different failure modes to remember.
func NewCipher(base64Key string, version int16) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("dbprovider: VAULT_ENCRYPTION_KEY must be valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("dbprovider: VAULT_ENCRYPTION_KEY must be 32 bytes (got %d)", len(key))
	}
	if version != CipherVersionLegacy && version != CipherVersion {
		return nil, fmt.Errorf("dbprovider: unsupported key version %d (want %d)", version, CipherVersion)
	}
	derived, err := hkdf.Key(sha256.New, key, []byte(cipherHKDFSalt), cipherHKDFInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("dbprovider: derive key: %w", err)
	}
	return &Cipher{raw: key, derived: derived, version: version}, nil
}

// Version returns the version new seals use — the value passed to
// NewCipher, recorded on every Seal so a later Open picks the right key.
func (c *Cipher) Version() int16 { return c.version }

func (c *Cipher) aead(version int16) (cipher.AEAD, []byte, error) {
	var key, aad []byte
	switch version {
	case CipherVersionLegacy:
		key = c.raw
	case CipherVersion:
		key, aad = c.derived, []byte(cipherAAD)
	default:
		// A stray version is data corruption or a botched rotation —
		// fail loud.
		return nil, nil, fmt.Errorf("dbprovider: unknown key version %d (current %d)", version, c.version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("dbprovider: aes cipher: %w", err)
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("dbprovider: gcm: %w", err)
	}
	return a, aad, nil
}

// Seal encrypts plaintext under the current version. Returns
// (ciphertext, nonce, version); persist all three alongside the row.
func (c *Cipher) Seal(plaintext string) (ciphertext, nonce []byte, version int16, err error) {
	a, aad, err := c.aead(c.version)
	if err != nil {
		return nil, nil, 0, err
	}
	nonce = make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, 0, fmt.Errorf("dbprovider: nonce: %w", err)
	}
	return a.Seal(nil, nonce, []byte(plaintext), aad), nonce, c.version, nil
}

// Open decrypts a row's ciphertext with the key of the version it was
// sealed with (legacy version 1 or the current one).
func (c *Cipher) Open(ciphertext, nonce []byte, version int16) (string, error) {
	a, aad, err := c.aead(version)
	if err != nil {
		return "", err
	}
	// GCM panics on a wrong nonce length; a malformed row is an error.
	if len(nonce) != a.NonceSize() {
		return "", fmt.Errorf("dbprovider: open: nonce is %d bytes, want %d", len(nonce), a.NonceSize())
	}
	plaintext, err := a.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", fmt.Errorf("dbprovider: open: %w", err)
	}
	return string(plaintext), nil
}
