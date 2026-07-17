package email

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The handler's GET path returns after rendering the confirm form
// WITHOUT touching the pool — that's the whole point of the
// safe-GET / mutating-POST split (bug #002). So these tests pass a
// nil pool and rely on that ordering.

func TestUnsubscribeHandler_GET_ValidToken_RendersConfirmForm(t *testing.T) {
	signer := NewUnsubscribeSigner("test-secret")
	token := signer.Sign("user-abc", "onboarding", time.Now().Add(1*time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/platform/mailing/unsubscribe?token="+token, nil)
	rec := httptest.NewRecorder()
	UnsubscribeHandler(signer, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<form method="post"`) {
		t.Error("expected POST form in body")
	}
	if !strings.Contains(body, `name="token"`) {
		t.Error("expected token hidden field")
	}
	if !strings.Contains(body, token) {
		t.Error("expected token value carried through to form")
	}
	// Confirm we're rendering the confirm page, not the result page.
	if strings.Contains(body, "You're unsubscribed") {
		t.Error("GET must not render the success page")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control: got %q want no-store", rec.Header().Get("Cache-Control"))
	}
	// The confirm page MUST override the gateway's default
	// `form-action 'none'` CSP, else the browser silently blocks
	// the POST when the user clicks Unsubscribe.
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("Content-Security-Policy missing form-action 'self': %q", csp)
	}
}

func TestUnsubscribeHandler_GET_MissingToken(t *testing.T) {
	signer := NewUnsubscribeSigner("test-secret")
	req := httptest.NewRequest(http.MethodGet, "/platform/mailing/unsubscribe", nil)
	rec := httptest.NewRecorder()
	UnsubscribeHandler(signer, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", rec.Code)
	}
}

func TestUnsubscribeHandler_GET_InvalidToken(t *testing.T) {
	signer := NewUnsubscribeSigner("test-secret")
	req := httptest.NewRequest(http.MethodGet, "/platform/mailing/unsubscribe?token=not-a-real-token", nil)
	rec := httptest.NewRecorder()
	UnsubscribeHandler(signer, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", rec.Code)
	}
}

func TestUnsubscribeHandler_GET_ExpiredToken(t *testing.T) {
	signer := NewUnsubscribeSigner("test-secret")
	token := signer.Sign("u1", "onboarding", time.Now().Add(-1*time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/platform/mailing/unsubscribe?token="+token, nil)
	rec := httptest.NewRecorder()
	UnsubscribeHandler(signer, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Errorf("status: got %d want 410", rec.Code)
	}
}

func TestUnsubscribeHandler_MethodNotAllowed(t *testing.T) {
	signer := NewUnsubscribeSigner("test-secret")
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(m, "/platform/mailing/unsubscribe", nil)
		rec := httptest.NewRecorder()
		UnsubscribeHandler(signer, nil).ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: got %d want 405", m, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Allow"), "GET") ||
			!strings.Contains(rec.Header().Get("Allow"), "POST") {
			t.Errorf("%s: Allow header missing GET/POST: %q", m, rec.Header().Get("Allow"))
		}
	}
}
