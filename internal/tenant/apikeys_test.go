package tenant

import (
	"strings"
	"testing"
)

func TestGenerateAPIKeyPair(t *testing.T) {
	publicKey, secretKey, publicKeyHash, secretKeyHash, err := GenerateAPIKeyPair()
	if err != nil {
		t.Fatalf("GenerateAPIKeyPair() returned error: %v", err)
	}

	// Public key: "eb_pk_" prefix (6 chars) + 32 hex chars = 38 chars.
	if !strings.HasPrefix(publicKey, "eb_pk_") {
		t.Errorf("public key should start with eb_pk_, got %q", publicKey)
	}
	if len(publicKey) != 38 {
		t.Errorf("public key should be 38 chars, got %d (%q)", len(publicKey), publicKey)
	}

	// Secret key: "eb_sk_" prefix (6 chars) + 32 hex chars = 38 chars.
	if !strings.HasPrefix(secretKey, "eb_sk_") {
		t.Errorf("secret key should start with eb_sk_, got %q", secretKey)
	}
	if len(secretKey) != 38 {
		t.Errorf("secret key should be 38 chars, got %d (%q)", len(secretKey), secretKey)
	}

	// Hashes should be 64 chars (hex-encoded SHA-256).
	if len(publicKeyHash) != 64 {
		t.Errorf("public key hash should be 64 chars, got %d", len(publicKeyHash))
	}
	if len(secretKeyHash) != 64 {
		t.Errorf("secret key hash should be 64 chars, got %d", len(secretKeyHash))
	}

	// Two calls should generate different keys.
	pub2, sec2, _, _, err := GenerateAPIKeyPair()
	if err != nil {
		t.Fatalf("second GenerateAPIKeyPair() returned error: %v", err)
	}
	if publicKey == pub2 {
		t.Error("two calls generated the same public key")
	}
	if secretKey == sec2 {
		t.Error("two calls generated the same secret key")
	}
}

func TestGenerateAPIKeyPairUniqueness(t *testing.T) {
	const n = 100
	publicKeys := make(map[string]struct{}, n)
	secretKeys := make(map[string]struct{}, n)

	for i := 0; i < n; i++ {
		pub, sec, _, _, err := GenerateAPIKeyPair()
		if err != nil {
			t.Fatalf("GenerateAPIKeyPair() iteration %d returned error: %v", i, err)
		}

		if _, exists := publicKeys[pub]; exists {
			t.Fatalf("duplicate public key at iteration %d: %s", i, pub)
		}
		publicKeys[pub] = struct{}{}

		if _, exists := secretKeys[sec]; exists {
			t.Fatalf("duplicate secret key at iteration %d: %s", i, sec)
		}
		secretKeys[sec] = struct{}{}
	}

	if len(publicKeys) != n {
		t.Errorf("expected %d unique public keys, got %d", n, len(publicKeys))
	}
	if len(secretKeys) != n {
		t.Errorf("expected %d unique secret keys, got %d", n, len(secretKeys))
	}
}
