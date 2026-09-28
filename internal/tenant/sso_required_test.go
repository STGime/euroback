package tenant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
)

// #710: the 403 names the org when the error carries it, so the console
// can start that org's SSO sign-in from a project route; errors.Is still
// matches the sentinel through wrapping.
func TestWriteSSORequired(t *testing.T) {
	err := fmt.Errorf("enforce: %w", &SSORequiredError{OrgID: "a580f347-aaa2-4d17-818e-fc3eb5e68af3"})
	if !errors.Is(err, ErrSSORequiredForOrg) {
		t.Fatal("errors.Is(SSORequiredError, ErrSSORequiredForOrg) = false")
	}
	rec := httptest.NewRecorder()
	WriteSSORequired(rec, err, "needs SSO")
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 403 || body["code"] != "sso_required_for_org" || body["org_id"] != "a580f347-aaa2-4d17-818e-fc3eb5e68af3" || body["error"] != "needs SSO" {
		t.Fatalf("got %d %v", rec.Code, body)
	}
	// The bare sentinel (no org known): no org_id key.
	rec = httptest.NewRecorder()
	WriteSSORequired(rec, ErrSSORequiredForOrg, "needs SSO")
	body = map[string]string{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if _, ok := body["org_id"]; ok || body["code"] != "sso_required_for_org" {
		t.Fatalf("sentinel: %v", body)
	}
}
