package tenant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// Project invites to reserved test domains are refused before anything
// is looked up or sent.
func TestHandleInviteMember_RefusesReservedAddress(t *testing.T) {
	h := HandleInviteMember(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"admin@example.com","role":"developer"}`))
	ctx := auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: "00000000-0000-0000-0000-000000000001"})
	req = req.WithContext(WithRole(ctx, "admin"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reserved test domain") {
		t.Fatalf("got %d %s, want 400 refusal", w.Code, w.Body.String())
	}
}
