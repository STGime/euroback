package enduser

import (
	"errors"
	"fmt"
	"testing"
)

// Closes advisory GHSA-269x-fqhj-x9jq — OAuth callback must not auto-link
// to an existing user by email. The new behaviour returns
// ErrAccountExistsLinkRequired which the handler maps to a distinct
// redirect error code.
//
// The full path (real OAuth provider exchange + tenant schema + user
// insert) is exercised by an integration test that requires a live DB.
// These unit tests cover the surface that doesn't need a database:
// sentinel identity and error-wrapping behaviour. They guard against
// accidental sentinel renaming or wrap-stripping in future refactors.

func TestErrAccountExistsLinkRequired_SentinelIsDetectable(t *testing.T) {
	if ErrAccountExistsLinkRequired == nil {
		t.Fatal("ErrAccountExistsLinkRequired must be a non-nil error value")
	}
	wrapped := fmt.Errorf("oauth callback: %w", ErrAccountExistsLinkRequired)
	if !errors.Is(wrapped, ErrAccountExistsLinkRequired) {
		t.Error("errors.Is must detect the sentinel through fmt.Errorf %w wrapping")
	}
	other := errors.New("something else")
	if errors.Is(other, ErrAccountExistsLinkRequired) {
		t.Error("errors.Is must NOT match an unrelated error against the sentinel")
	}
}

func TestErrAccountExistsLinkRequired_MessageMentionsLink(t *testing.T) {
	// The error message is surfaced to the OAuth client as the
	// error_description query parameter on the redirect. It should
	// guide the user toward signing in with their existing credentials
	// rather than reading like a generic OAuth failure.
	msg := ErrAccountExistsLinkRequired.Error()
	for _, want := range []string{"account exists", "settings"} {
		if !contains(msg, want) {
			t.Errorf("error message %q should mention %q", msg, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
