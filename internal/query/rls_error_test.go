package query

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// Closes #77 (part 2): an RLS WITH CHECK violation must not leak as an
// opaque 500 — it's an authorization failure and the developer needs to
// see which policy denied the write.

func TestHandleQueryError_RLSViolation_Returns403WithPolicyName(t *testing.T) {
	err := errors.New(`ERROR: new row violates row-level security policy "own_things" for table "things" (SQLSTATE 42501)`)
	rr := httptest.NewRecorder()
	if !handleQueryError(rr, err) {
		t.Fatal("expected handleQueryError to recognise an RLS violation")
	}
	if rr.Code != 403 {
		t.Errorf("status = %d, want 403", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "row-level security policy denied") {
		t.Errorf("body should explain RLS denial, got %q", body)
	}
	if !strings.Contains(body, `"policy":"own_things"`) {
		t.Errorf("body should include the policy name, got %q", body)
	}
}

func TestHandleQueryError_RLSViolation_NoPolicyNameStillSurfaces403(t *testing.T) {
	// Defensive: if the message format changes and no quoted name is
	// present, we still want a 403 so the user sees an authz error
	// rather than a 500 outage.
	err := errors.New("ERROR: new row violates row-level security policy (SQLSTATE 42501)")
	rr := httptest.NewRecorder()
	if !handleQueryError(rr, err) {
		t.Fatal("expected handleQueryError to recognise an RLS violation without a policy name")
	}
	if rr.Code != 403 {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestExtractRLSPolicy_PullsQuotedName(t *testing.T) {
	cases := map[string]string{
		`new row violates row-level security policy "own_things" for table "things"`: "own_things",
		`new row violates row-level security policy "Users can CRUD own X" for table "x"`: "Users can CRUD own X",
		`new row violates row-level security policy (SQLSTATE 42501)`:                "",
	}
	for msg, want := range cases {
		got := extractRLSPolicy(errors.New(msg))
		if got != want {
			t.Errorf("extractRLSPolicy(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestIsRLSViolation_DoesNotMatchUnrelatedErrors(t *testing.T) {
	for _, msg := range []string{
		"duplicate key value violates unique constraint (SQLSTATE 23505)",
		"null value in column violates not-null constraint (SQLSTATE 23502)",
		"connection refused",
	} {
		if isRLSViolation(errors.New(msg)) {
			t.Errorf("isRLSViolation(%q) = true, expected false", msg)
		}
	}
}
