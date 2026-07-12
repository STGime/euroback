package auth

import (
	"strings"
	"testing"
)

// Phase A of the public-beta launch plan. `validateRequiredAcceptances`
// is the click-through gate — a pure function so we pin the behaviour
// without spinning up Postgres.

func TestValidateRequiredAcceptances_AllPresent(t *testing.T) {
	err := validateRequiredAcceptances([]AcceptedDocument{
		{Type: "terms", Version: "2.0"},
		{Type: "dpa", Version: "2.0"},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateRequiredAcceptances_MissingTerms(t *testing.T) {
	err := validateRequiredAcceptances([]AcceptedDocument{
		{Type: "dpa", Version: "2.0"},
	})
	if err == nil {
		t.Fatal("expected error when Terms missing")
	}
	if !strings.Contains(err.Error(), "terms") {
		t.Errorf("error should name the missing doc: %v", err)
	}
}

func TestValidateRequiredAcceptances_MissingDPA(t *testing.T) {
	err := validateRequiredAcceptances([]AcceptedDocument{
		{Type: "terms", Version: "2.0"},
	})
	if err == nil {
		t.Fatal("expected error when DPA missing")
	}
	if !strings.Contains(err.Error(), "dpa") {
		t.Errorf("error should name the missing doc: %v", err)
	}
}

func TestValidateRequiredAcceptances_Empty(t *testing.T) {
	err := validateRequiredAcceptances(nil)
	if err == nil {
		t.Fatal("expected error on empty acceptances")
	}
	// Both required docs should be named.
	msg := err.Error()
	if !strings.Contains(msg, "terms") || !strings.Contains(msg, "dpa") {
		t.Errorf("error should name both missing docs, got: %v", err)
	}
}

func TestValidateRequiredAcceptances_CaseInsensitive(t *testing.T) {
	// A hostile / typo'd client sending 'Terms' and 'DPA' must still
	// satisfy the gate — we treat document_type as case-insensitive
	// throughout (matched with strings.ToLower in resolve too).
	err := validateRequiredAcceptances([]AcceptedDocument{
		{Type: "Terms", Version: "2.0"},
		{Type: "DPA", Version: "2.0"},
	})
	if err != nil {
		t.Errorf("case-insensitive matching failed: %v", err)
	}
}

// #279 review high #1: ErrStaleDocumentVersion is the typed sentinel
// the HTTP layer switches on to return 400. If someone renames the
// struct or changes the error() text, this test fails loudly so the
// handler branch below (which matches by type) stays in sync.
func TestErrStaleDocumentVersion_TypedSentinel(t *testing.T) {
	err := &ErrStaleDocumentVersion{DocumentType: "terms", Version: "1.0"}
	// Type-assert path — HTTP layer relies on this exact shape.
	if _, ok := interface{}(err).(*ErrStaleDocumentVersion); !ok {
		t.Fatal("ErrStaleDocumentVersion type assertion broken")
	}
	// Human-readable text includes both fields so the console banner
	// can display something specific ("terms v1.0 is out of date").
	msg := err.Error()
	if !strings.Contains(msg, "terms") || !strings.Contains(msg, "1.0") {
		t.Errorf("error message should name the doc + version: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "refresh") {
		t.Errorf("error message should tell the user to refresh: %q", msg)
	}
}

func TestValidateRequiredAcceptances_ExtrasAllowed(t *testing.T) {
	// Accepting privacy/aup/cookies too doesn't fail — only the
	// required set is gated. Extras get recorded downstream by
	// resolveAcceptedDocuments; here we just check the gate.
	err := validateRequiredAcceptances([]AcceptedDocument{
		{Type: "terms", Version: "2.0"},
		{Type: "dpa", Version: "2.0"},
		{Type: "privacy", Version: "2.0"},
		{Type: "aup", Version: "2.0"},
	})
	if err != nil {
		t.Errorf("extras should be allowed: %v", err)
	}
}
