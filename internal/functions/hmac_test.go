package functions

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Closes layer 3 of advisory GHSA-7428-mvpp-rhr7. These tests verify
// the Go signer / verifier pair is internally consistent. The
// cross-language compatibility (Go signs, Deno verifies — and vice
// versa) is asserted by a fixed reference vector at the bottom; if
// either side's canonical-message format drifts, the vector breaks.

const testSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

func makeTestHeaders() http.Header {
	h := http.Header{}
	h.Set("X-Project-ID", "p-001")
	h.Set("X-Schema-Name", "tenant_abc")
	h.Set("X-Function-ID", "fn-deadbeef")
	h.Set("X-User-ID", "u-1234")
	h.Set("X-User-Email", "alice@example.com")
	h.Set("X-Plan", "pro")
	h.Set("X-Request-ID", "req-xyz")
	return h
}

func TestNewSigner_RejectsShortSecret(t *testing.T) {
	for _, short := range []string{"", "abc", strings.Repeat("a", 31)} {
		if _, err := NewSigner(short); err == nil {
			t.Errorf("NewSigner(%q) expected error, got nil", short)
		}
	}
}

func TestNewSigner_AcceptsAdequateSecret(t *testing.T) {
	if _, err := NewSigner(testSecret); err != nil {
		t.Errorf("NewSigner(32-byte) failed: %v", err)
	}
}

func TestSign_AddsTimestampAndSignatureHeaders(t *testing.T) {
	signer, _ := NewSigner(testSecret)
	h := makeTestHeaders()
	signer.Sign(h, time.Unix(1700000000, 0))

	if h.Get("X-Eurobase-Timestamp") != "1700000000" {
		t.Errorf("expected timestamp 1700000000, got %q", h.Get("X-Eurobase-Timestamp"))
	}
	sig := h.Get("X-Eurobase-Signature")
	if len(sig) != 64 {
		t.Errorf("expected 64-hex-char signature, got %d chars: %q", len(sig), sig)
	}
}

func TestSignVerify_RoundTrip(t *testing.T) {
	signer, _ := NewSigner(testSecret)
	h := makeTestHeaders()
	signer.Sign(h, time.Now())

	if err := signer.Verify(h, VerifyOptions{}); err != nil {
		t.Errorf("Verify of self-signed headers failed: %v", err)
	}
}

func TestVerify_RejectsTamperedHeader(t *testing.T) {
	signer, _ := NewSigner(testSecret)
	h := makeTestHeaders()
	signer.Sign(h, time.Now())

	// Flip the schema name. Verifier should detect.
	h.Set("X-Schema-Name", "tenant_other")
	if err := signer.Verify(h, VerifyOptions{}); !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("Verify of tampered headers should return ErrSignatureMismatch, got %v", err)
	}
}

func TestVerify_RejectsMissingSignature(t *testing.T) {
	signer, _ := NewSigner(testSecret)
	h := makeTestHeaders()
	// No Sign() call — headers have no signature.
	err := signer.Verify(h, VerifyOptions{})
	if !errors.Is(err, ErrMissingSignature) {
		t.Errorf("Verify of unsigned headers should return ErrMissingSignature, got %v", err)
	}
}

func TestVerify_RejectsExpiredTimestamp(t *testing.T) {
	signer, _ := NewSigner(testSecret)
	h := makeTestHeaders()
	signer.Sign(h, time.Unix(1700000000, 0)) // signature is valid for that ts

	// "Now" is well outside the 5-min skew window.
	err := signer.Verify(h, VerifyOptions{
		MaxClockSkew: 5 * time.Minute,
		Now:          func() time.Time { return time.Unix(1700001000, 0) }, // +1000s
	})
	if !errors.Is(err, ErrTimestampOutOfWindow) {
		t.Errorf("Verify of expired timestamp should return ErrTimestampOutOfWindow, got %v", err)
	}
}

func TestVerify_RejectsFutureTimestamp(t *testing.T) {
	signer, _ := NewSigner(testSecret)
	h := makeTestHeaders()
	signer.Sign(h, time.Unix(1700001000, 0))

	err := signer.Verify(h, VerifyOptions{
		MaxClockSkew: 5 * time.Minute,
		Now:          func() time.Time { return time.Unix(1700000000, 0) }, // -1000s
	})
	if !errors.Is(err, ErrTimestampOutOfWindow) {
		t.Errorf("Verify of future timestamp should return ErrTimestampOutOfWindow, got %v", err)
	}
}

func TestVerify_RejectsBadTimestampFormat(t *testing.T) {
	signer, _ := NewSigner(testSecret)
	h := makeTestHeaders()
	signer.Sign(h, time.Now())
	h.Set("X-Eurobase-Timestamp", "not-a-number")
	if err := signer.Verify(h, VerifyOptions{}); !errors.Is(err, ErrTimestampOutOfWindow) {
		t.Errorf("expected ErrTimestampOutOfWindow on bad timestamp, got %v", err)
	}
}

func TestVerify_AcceptsWithinSkewWindow(t *testing.T) {
	signer, _ := NewSigner(testSecret)
	h := makeTestHeaders()
	signer.Sign(h, time.Unix(1700000000, 0))

	// 4 minutes 30 seconds later — within 5-min window.
	err := signer.Verify(h, VerifyOptions{
		MaxClockSkew: 5 * time.Minute,
		Now:          func() time.Time { return time.Unix(1700000270, 0) },
	})
	if err != nil {
		t.Errorf("Verify within skew window should succeed, got %v", err)
	}
}

func TestVerify_DifferentSecretFails(t *testing.T) {
	signer1, _ := NewSigner(testSecret)
	signer2, _ := NewSigner("ffffffffffffffffffffffffffffffff") // 32 chars but different key
	h := makeTestHeaders()
	signer1.Sign(h, time.Now())
	if err := signer2.Verify(h, VerifyOptions{}); !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("Verify with different secret should return ErrSignatureMismatch, got %v", err)
	}
}

func TestSign_OrderOfHeadersDoesNotMatter(t *testing.T) {
	// http.Header iteration is map-ordered; canonicalMessage uses
	// fixed lookup keys so the map iteration order can't affect the
	// output. Two signers seeded with the same secret should produce
	// the same signature for the same logical input regardless of
	// which order the test sets the keys.
	signer, _ := NewSigner(testSecret)
	now := time.Unix(1700000000, 0)

	a := http.Header{}
	a.Set("X-User-Email", "alice@example.com")
	a.Set("X-Project-ID", "p-001")
	a.Set("X-Schema-Name", "tenant_abc")
	a.Set("X-Function-ID", "fn-deadbeef")
	a.Set("X-User-ID", "u-1234")
	a.Set("X-Plan", "pro")
	a.Set("X-Request-ID", "req-xyz")
	signer.Sign(a, now)

	b := makeTestHeaders()
	signer.Sign(b, now)

	if a.Get("X-Eurobase-Signature") != b.Get("X-Eurobase-Signature") {
		t.Errorf("signatures should match regardless of header set order; got %q vs %q",
			a.Get("X-Eurobase-Signature"), b.Get("X-Eurobase-Signature"))
	}
}

// TestCanonicalMessage_ReferenceVector is the cross-language compat
// pin. If this changes, the runner's `functions-runner/hmac.ts` MUST
// change in lockstep — otherwise gateway-signed requests fail to verify.
func TestCanonicalMessage_ReferenceVector(t *testing.T) {
	h := makeTestHeaders()
	got := canonicalMessage(h, "1700000000")
	want := "v=1\n" +
		"ts=1700000000\n" +
		"project=p-001\n" +
		"schema=tenant_abc\n" +
		"function=fn-deadbeef\n" +
		"user=u-1234\n" +
		"email=alice@example.com\n" +
		"plan=pro\n" +
		"requestid=req-xyz"
	if got != want {
		t.Errorf("canonicalMessage drift detected.\n got:\n%q\n want:\n%q", got, want)
	}
}

func TestCanonicalMessage_EmptyValuesEmittedAsEmpty(t *testing.T) {
	// X-User-ID / X-User-Email are blank for unauthenticated requests.
	// They MUST be present in the canonical message as empty strings,
	// not omitted — otherwise a forged request that adds an arbitrary
	// X-User-ID could mutate the canonical form to match a different
	// signature.
	h := http.Header{}
	h.Set("X-Project-ID", "p")
	h.Set("X-Schema-Name", "t")
	h.Set("X-Function-ID", "f")
	h.Set("X-Plan", "free")
	h.Set("X-Request-ID", "r")
	got := canonicalMessage(h, "100")
	if !strings.Contains(got, "user=\n") {
		t.Errorf("expected user= line with empty value, got:\n%s", got)
	}
	if !strings.Contains(got, "email=\n") {
		t.Errorf("expected email= line with empty value, got:\n%s", got)
	}
}
