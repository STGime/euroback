package dbprovider

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// testKey is a 32-byte all-zeros master key encoded to base64.
// Deterministic seals in tests use this constant.
const testKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 zero bytes

func TestCipher_RoundTrip(t *testing.T) {
	c, err := NewCipher(testKey, 1)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	plaintext := "not-a-real-password-42"
	ct, nonce, ver, err := c.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if ver != 1 {
		t.Errorf("version: got %d, want 1", ver)
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
	c, _ := NewCipher(testKey, 1)
	ct, nonce, _, _ := c.Seal("x")
	_, err := c.Open(ct, nonce, 2)
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
		if _, err := NewCipher(key, 1); err == nil {
			t.Errorf("%s: NewCipher should have errored", name)
		}
	}
}

func TestCipher_RejectsZeroVersion(t *testing.T) {
	if _, err := NewCipher(testKey, 0); err == nil {
		t.Error("NewCipher(v=0) should have errored")
	}
}
