package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGmailCanonical pins the Gmail dot-and-plus-tag normalization
// used to salvage SSO logins where the user's Eurobase email uses a
// different form (dot placement, +tag) than the canonical email
// Google returns in the OIDC id_token. Non-Gmail addresses return
// "" so the caller knows to fall through instead of matching too
// broadly (e.g. we do NOT strip dots from "acme.com" addresses —
// those are different mailboxes).
func TestGmailCanonical(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Gmail: dots stripped, +tag stripped, googlemail folds to gmail.
		{"stefan.gimeson@gmail.com", "stefangimeson@gmail.com"},
		{"stefangimeson@gmail.com", "stefangimeson@gmail.com"},
		{"stefan.gimeson+foo@gmail.com", "stefangimeson@gmail.com"},
		{"stefan+foo@googlemail.com", "stefan@gmail.com"},
		{"a.b.c.d@gmail.com", "abcd@gmail.com"},
		{"A.B@GMAIL.COM", ""},           // caller lowercases; we only canonicalize post-lower
		{"acme@example.com", ""},        // not Gmail
		{"bad", ""},                     // no @
		{"@gmail.com", ""},              // empty local part
		{"+tag@gmail.com", ""},          // local part is only a tag
	}
	for _, tc := range cases {
		got := gmailCanonical(tc.in)
		if got != tc.want {
			t.Errorf("gmailCanonical(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestRedirectWithError_NoDoubleEncoding pins the fix for the
// bug spotted in prod on 2026-09-10: the em-dash in the
// "not_a_member" error message rendered as `%E2%80%94` in the
// console banner because Go's URL package double-encoded the
// fragment. `url.Values.Encode()` produced `%E2%80%94`, we set it
// as `u.Fragment`, then `u.String()` re-escaped the `%` as `%25`,
// yielding `%25E2%2580%2594`. The browser's URLSearchParams then
// only decodes one layer.
//
// The regression test is: build a full redirect URL through the
// error path, parse it as a browser would (URL → fragment →
// URLSearchParams), and assert the decoded message contains the
// original em-dash (`—`, U+2014), not the percent-encoded bytes.
func TestRedirectWithError_NoDoubleEncoding(t *testing.T) {
	h := &SSOHandler{
		cfg: SSOConfig{ConsoleRedirectURL: "https://console.eurobase.app/login"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/platform/auth/sso/callback", nil)

	// Trigger the error path with the exact message that surfaced
	// in prod — contains a UTF-8 em-dash (U+2014).
	h.redirectWithError(w, r, "not_a_member", "you are not a member of this organization — ask an admin to invite you")

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("no Location header on redirect")
	}

	// Simulate the browser faithfully. `url.Parse().Fragment`
	// decodes one layer, which would MASK the double-encoding
	// bug — the browser's `window.location.hash` returns the
	// fragment RAW (not decoded), and then URLSearchParams
	// decodes exactly ONCE. So we extract the fragment string
	// ourselves (raw substring after `#`) and hand it to
	// ParseQuery, which is the URLSearchParams equivalent.
	hashIdx := strings.Index(loc, "#")
	if hashIdx < 0 {
		t.Fatal("redirect URL has no fragment")
	}
	if !strings.Contains(loc[:hashIdx], "/login") {
		t.Errorf("redirect should land on /login, got %q", loc[:hashIdx])
	}
	rawFragment := loc[hashIdx+1:]
	frag, err := url.ParseQuery(rawFragment)
	if err != nil {
		t.Fatalf("fragment failed to parse as query: %v", err)
	}
	gotMsg := frag.Get("sso_error_msg")
	if gotMsg == "" {
		t.Fatal("sso_error_msg missing from fragment")
	}
	// The whole point of the fix: the message must decode cleanly
	// to the original UTF-8 em-dash. If we regress the double-
	// encoding, this string will contain `%E2%80%94` instead.
	if !strings.Contains(gotMsg, "—") {
		t.Errorf("expected message to contain em-dash '—'; got %q", gotMsg)
	}
	if strings.Contains(gotMsg, "%E2%80%94") || strings.Contains(gotMsg, "%25") {
		t.Errorf("fragment appears double-encoded — got %q", gotMsg)
	}
	if code := frag.Get("sso_error"); code != "not_a_member" {
		t.Errorf("sso_error code lost or wrong: %q", code)
	}
}
