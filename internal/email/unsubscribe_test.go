package email

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestUnsubscribeSigner_RoundTrip is the happy path: sign then verify
// returns the same user + category with no error.
func TestUnsubscribeSigner_RoundTrip(t *testing.T) {
	s := NewUnsubscribeSigner("test-platform-jwt-secret-that-is-long-enough")
	token := s.Sign("user-123", "onboarding", time.Now().Add(1*time.Hour))
	userID, category, err := s.Verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if userID != "user-123" {
		t.Errorf("userID: got %q want user-123", userID)
	}
	if category != "onboarding" {
		t.Errorf("category: got %q want onboarding", category)
	}
}

func TestUnsubscribeSigner_Expired(t *testing.T) {
	s := NewUnsubscribeSigner("test-secret")
	token := s.Sign("u1", "onboarding", time.Now().Add(-1*time.Minute))
	_, _, err := s.Verify(token)
	if !errors.Is(err, ErrExpiredUnsubscribeToken) {
		t.Fatalf("expected ErrExpiredUnsubscribeToken, got %v", err)
	}
}

func TestUnsubscribeSigner_TamperedPayload(t *testing.T) {
	s := NewUnsubscribeSigner("test-secret")
	token := s.Sign("user-a", "onboarding", time.Now().Add(1*time.Hour))
	// Flip the last character of the token; base64 is 1:1 alphabet-mapped
	// so this reliably corrupts the signature or payload.
	tampered := token[:len(token)-1] + "A"
	if tampered == token {
		tampered = token[:len(token)-1] + "B"
	}
	_, _, err := s.Verify(tampered)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
}

func TestUnsubscribeSigner_WrongKey(t *testing.T) {
	signer := NewUnsubscribeSigner("secret-a")
	other := NewUnsubscribeSigner("secret-b")
	token := signer.Sign("u1", "onboarding", time.Now().Add(1*time.Hour))
	_, _, err := other.Verify(token)
	if !errors.Is(err, ErrInvalidUnsubscribeToken) {
		t.Fatalf("expected ErrInvalidUnsubscribeToken, got %v", err)
	}
}

func TestUnsubscribeSigner_MalformedToken(t *testing.T) {
	s := NewUnsubscribeSigner("test-secret")
	cases := []string{
		"",
		"not-base64!!!",
		"YQ",              // valid base64 but only 1 field after decode
		"YXxifGN8ZA",      // "a|b|c|d" — 4 fields but "c" isn't a number and sig invalid
	}
	for _, c := range cases {
		_, _, err := s.Verify(c)
		if err == nil {
			t.Errorf("%q: expected error, got nil", c)
		}
	}
}

func TestUnsubscribeSigner_UnknownCategory(t *testing.T) {
	s := NewUnsubscribeSigner("test-secret")
	// A signature-valid token for a category not in the allowlist
	// must be rejected — protects against a stale-key forger picking
	// a novel category name to bypass mailing_preferences CHECK.
	token := s.Sign("u1", "bogus_category", time.Now().Add(1*time.Hour))
	_, _, err := s.Verify(token)
	if !errors.Is(err, ErrInvalidUnsubscribeToken) {
		t.Fatalf("expected ErrInvalidUnsubscribeToken for unknown category, got %v", err)
	}
}

func TestBuildUnsubscribeURL(t *testing.T) {
	s := NewUnsubscribeSigner("test-secret")
	url := BuildUnsubscribeURL(s, "https://api.eurobase.app/", "u1", "onboarding")
	// Trailing slash on baseURL should be trimmed — one slash between
	// host and path, not two.
	if strings.Contains(url, "app//") {
		t.Errorf("double slash in URL: %s", url)
	}
	if !strings.HasPrefix(url, "https://api.eurobase.app/platform/mailing/unsubscribe?token=") {
		t.Errorf("unexpected URL shape: %s", url)
	}
	// Token should round-trip via Verify.
	prefix := "token="
	i := strings.Index(url, prefix)
	if i < 0 {
		t.Fatalf("no token in %s", url)
	}
	tok := url[i+len(prefix):]
	userID, category, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("verify embedded token: %v", err)
	}
	if userID != "u1" || category != "onboarding" {
		t.Errorf("round-trip mismatch: %s %s", userID, category)
	}
}
