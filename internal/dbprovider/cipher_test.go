package dbprovider

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"strings"
	"testing"
)

// testKey is a 32-byte all-zeros master key encoded to base64.
// Deterministic seals in tests use this constant.
const testKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 zero bytes

func TestCipher_RoundTrip(t *testing.T) {
	c, err := NewCipher(testKey, CipherVersion)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	plaintext := "not-a-real-password-42"
	ct, nonce, ver, err := c.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if ver != CipherVersion {
		t.Errorf("version: got %d, want %d", ver, CipherVersion)
	}
	if len(nonce) != 12 {
		t.Errorf("nonce length: got %d, want 12 (GCM standard)", len(nonce))
	}
	if bytes.Contains(ct, []byte(plaintext)) {
		t.Error("ciphertext contains plaintext bytes — encryption not applied")
	}
	got, err := c.Open(ct, nonce, ver)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != plaintext {
		t.Errorf("plaintext: got %q, want %q", got, plaintext)
	}
}

func TestCipher_UniqueNoncePerSeal(t *testing.T) {
	c, _ := NewCipher(testKey, 1)
	// Seal the same plaintext twice; nonce must differ so ciphertexts
	// are different — otherwise a passive observer could correlate
	// equal passwords across two rows.
	_, n1, _, _ := c.Seal("same-password")
	_, n2, _, _ := c.Seal("same-password")
	if bytes.Equal(n1, n2) {
		t.Fatal("nonce reused across two seals — GCM security violated")
	}
}

func TestCipher_RejectsWrongVersion(t *testing.T) {
	c, _ := NewCipher(testKey, CipherVersion)
	ct, nonce, _, _ := c.Seal("x")
	_, err := c.Open(ct, nonce, 3)
	if err == nil || !strings.Contains(err.Error(), "unknown key version") {
		t.Errorf("wrong-version Open should fail with 'unknown key version', got %v", err)
	}
}

func TestCipher_RejectsMalformedKey(t *testing.T) {
	cases := map[string]string{
		"invalid base64": "not-base64!!!",
		"wrong length":   base64.StdEncoding.EncodeToString([]byte("short")),
	}
	for name, key := range cases {
		if _, err := NewCipher(key, CipherVersion); err == nil {
			t.Errorf("%s: NewCipher should have errored", name)
		}
	}
}

func TestCipher_RejectsZeroVersion(t *testing.T) {
	if _, err := NewCipher(testKey, 0); err == nil {
		t.Error("NewCipher(v=0) should have errored")
	}
}

// Version 2 is not the raw master key: a raw-key AES-GCM open (what the
// vault does for a legacy key_version 0 row) can't read it.
func TestCipher_V2NotOpenableWithRawKey(t *testing.T) {
	c, _ := NewCipher(testKey, CipherVersion)
	ct, nonce, _, _ := c.Seal("owner-password")
	raw, _ := base64.StdEncoding.DecodeString(testKey)
	block, _ := aes.NewCipher(raw)
	gcm, _ := cipher.NewGCM(block)
	if _, err := gcm.Open(nil, nonce, ct, nil); err == nil {
		t.Fatal("a version 2 ciphertext opens with the raw master key")
	}
	// Nor with the right key but without the associated data.
	block, _ = aes.NewCipher(c.derived)
	gcm, _ = cipher.NewGCM(block)
	if _, err := gcm.Open(nil, nonce, ct, nil); err == nil {
		t.Fatal("a version 2 ciphertext opens without its associated data")
	}
}

// Rows sealed before version 2 keep opening until ResealLegacy moves them.
func TestCipher_OpensLegacyV1(t *testing.T) {
	legacy, _ := NewCipher(testKey, CipherVersionLegacy)
	ct, nonce, ver, _ := legacy.Seal("old-password")
	if ver != CipherVersionLegacy {
		t.Fatalf("legacy seal version %d", ver)
	}
	c, _ := NewCipher(testKey, CipherVersion)
	got, err := c.Open(ct, nonce, ver)
	if err != nil || got != "old-password" {
		t.Fatalf("open legacy: %q, %v", got, err)
	}
	// Version labels are not interchangeable.
	if _, err := c.Open(ct, nonce, CipherVersion); err == nil {
		t.Fatal("a legacy ciphertext opens as version 2")
	}
}

// A malformed row (wrong nonce length) is an error, not a panic.
func TestCipher_MalformedNonceIsError(t *testing.T) {
	c, _ := NewCipher(testKey, CipherVersion)
	for _, v := range []int16{CipherVersionLegacy, CipherVersion} {
		if _, err := c.Open([]byte("x"), []byte("x"), v); err == nil {
			t.Fatalf("v%d: malformed nonce opened", v)
		}
	}
}

// Rollback safety: a component still writing the legacy version (step 1,
// or step 2 rolled back) opens version 2 rows the re-seal already wrote.
func TestCipher_LegacyWriterOpensV2(t *testing.T) {
	cur, _ := NewCipher(testKey, CipherVersion)
	ct, nonce, ver, _ := cur.Seal("resealed")
	legacy, _ := NewCipher(testKey, CipherVersionLegacy)
	if got, err := legacy.Open(ct, nonce, ver); err != nil || got != "resealed" {
		t.Fatalf("legacy writer opening v2: %q, %v", got, err)
	}
}
