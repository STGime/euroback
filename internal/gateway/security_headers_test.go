package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Closes #54. Every response leaving the gateway must carry the
// deny-by-default headers, including error paths.

func runSecurityHeaders(t *testing.T, downstreamStatus int) http.Header {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(downstreamStatus)
	})
	srv := httptest.NewServer(SecurityHeadersMiddleware(inner))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.Header
}

func TestSecurityHeaders_AllSetOnSuccess(t *testing.T) {
	h := runSecurityHeaders(t, http.StatusOK)
	for k, want := range map[string]string{
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Content-Security-Policy":   apiCSP,
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s: got %q, want %q", k, got, want)
		}
	}
}

func TestSecurityHeaders_AllSetOnError(t *testing.T) {
	// Defense-in-depth headers must also land on 4xx/5xx — those are
	// exactly the responses an attacker is most likely to probe.
	h := runSecurityHeaders(t, http.StatusInternalServerError)
	if h.Get("Content-Security-Policy") == "" {
		t.Error("CSP missing on 500 response")
	}
	if h.Get("X-Frame-Options") != "DENY" {
		t.Error("X-Frame-Options missing on 500 response")
	}
}

func TestApiCSP_DenyByDefault(t *testing.T) {
	// Spot-check that the CSP string actually starts with default-src
	// 'none' — a future edit that loosens this should fail the test.
	if !strings.HasPrefix(apiCSP, "default-src 'none'") {
		t.Errorf("apiCSP must start with default-src 'none'; got %q", apiCSP)
	}
	for _, must := range []string{"frame-ancestors 'none'", "base-uri 'none'", "form-action 'none'"} {
		if !strings.Contains(apiCSP, must) {
			t.Errorf("apiCSP missing %q; full policy: %q", must, apiCSP)
		}
	}
}
