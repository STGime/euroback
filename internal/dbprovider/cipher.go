package dbprovider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// Cipher seals and opens the DB password stored inline on
// project_databases (ciphertext + nonce + key_version columns).
//
// Uses AES-256-GCM with a 32-byte master key sourced from
// VAULT_ENCRYPTION_KEY — the same secret the vault package uses. No
// new secret is introduced; we simply seal a different class of
// value against it. Rotation is handled via the key_version column:
// when a new master key is rolled, existing rows keep opening with
// their recorded version until re-sealed.
type Cipher struct {
	key     []byte
	version int16
}

// NewCipher builds a Cipher from a base64-encoded 32-byte master
// key. Mirrors vault.NewVaultService's key-validation shape so ops
// don't have two different failure modes to remember.
func NewCipher(base64Key string, version int16) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("dbprovider: VAULT_ENCRYPTION_KEY must be valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("dbprovider: VAULT_ENCRYPTION_KEY must be 32 bytes (got %d)", len(key))
	}
	if version < 1 {
		return nil, fmt.Errorf("dbprovider: key version must be >= 1 (got %d)", version)
	}
	return &Cipher{key: key, version: version}, nil
}

// Version returns the current write-version — always the value
// passed to NewCipher. Recorded on every Seal so a later Open can
// look up the correct key even after rotation.
func (c *Cipher) Version() int16 { return c.version }

// Seal encrypts plaintext under the current key. Returns
// (ciphertext, nonce, version); persist all three alongside the row.
func (c *Cipher) Seal(plaintext string) (ciphertext, nonce []byte, version int16, err error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("dbprovider: aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("dbprovider: gcm: %w", err)
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, 0, fmt.Errorf("dbprovider: nonce: %w", err)
	}
	ciphertext = aead.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, nonce, c.version, nil
}

// Open decrypts a row's ciphertext. Takes the version explicitly so
// that rotation-mid-read is a no-op — the cipher looks up the right
// key for the version. Today we only carry v1, so any non-1 version
// returns an error.
func (c *Cipher) Open(ciphertext, nonce []byte, version int16) (string, error) {
	if version != c.version {
		// Once we support rotation, this becomes a key-lookup call.
		// For M1 we only support one live version — a stray v0/v2
		// row is either data corruption or a botched rotation, both
		// worth failing loud.
		return "", fmt.Errorf("dbprovider: unknown key version %d (current %d)", version, c.version)
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("dbprovider: aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("dbprovider: gcm: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("dbprovider: open: %w", err)
	}
	return string(plaintext), nil
}
