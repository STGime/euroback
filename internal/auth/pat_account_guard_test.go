package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Account-level actions refuse personal access tokens before touching the
// service (nil here): a token handed to a tool must not delete the account,
// change the password or manage tokens. A console session (JWT) reaches the
// handler as before.
func TestAccountActionsRefusePATs(t *testing.T) {
	cases := []struct {
		name string
		h    http.HandlerFunc
		body string
	}{
		{"delete account", HandleDeleteAccount(nil), `{"confirmation_email":"a@example.com"}`},
		{"change password", HandleChangePassword(nil), `{"current_password":"x","new_password":"y"}`},
		{"list tokens", HandleListPATs(nil), ``},
		{"revoke token", HandleRevokePAT(nil), ``},
		{"create token", HandleCreatePAT(nil), `{"name":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer "+PATPrefix+"abcdefabcdefabcdef")
			req = req.WithContext(ContextWithClaims(context.Background(), &Claims{Subject: "u1", Email: "a@example.com"}))
			rec := httptest.NewRecorder()
			c.h(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "personal access tokens cannot") {
				t.Fatalf("PAT: status %d body %s, want 403", rec.Code, rec.Body.String())
			}
		})
	}
}

// With a console session the guard lets the request through (the handler
// then fails on its own validation here — the point is it isn't a 403).
func TestAccountActionsAllowConsoleSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"confirmation_email":"other@example.com"}`))
	req.Header.Set("Authorization", "Bearer eyJhbGciOi.jwt.session")
	req = req.WithContext(ContextWithClaims(context.Background(), &Claims{Subject: "u1", Email: "a@example.com"}))
	rec := httptest.NewRecorder()
	HandleDeleteAccount(nil)(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("console session refused: %s", rec.Body.String())
	}
}

// RequireConsoleSession refuses a token — recognised from the claims set
// where the token was validated, even without the header — and lets a
// console session through.
func TestRequireConsoleSession(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, c := range []struct {
		name   string
		claims *Claims
		header string
		want   int
	}{
		{"pat claims", &Claims{Subject: "u1", LoginVia: LoginViaPAT}, "", http.StatusForbidden},
		{"pat header", &Claims{Subject: "u1"}, "Bearer " + PATPrefix + "abcdef", http.StatusForbidden},
		{"password session", &Claims{Subject: "u1", LoginVia: LoginViaPassword}, "Bearer eyJ.jwt", http.StatusNoContent},
		{"sso session", &Claims{Subject: "u1", LoginVia: LoginViaSSO}, "Bearer eyJ.jwt", http.StatusNoContent},
	} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		req = req.WithContext(ContextWithClaims(context.Background(), c.claims))
		rec := httptest.NewRecorder()
		RequireConsoleSession(next).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

// A session from an org's SSO (the org admin runs that IdP) can't act for
// the person: manage tokens, delete / re-secure the account, or accept
// invitations. A password or passkey session can.
func TestPersonalActionsRefuseSSOSessions(t *testing.T) {
	sso := &Claims{Subject: "u1", Email: "a@example.com", LoginVia: LoginViaSSO, SsoOrgID: "o1"}
	for _, c := range []struct {
		name string
		h    http.Handler
	}{
		{"delete account", HandleDeleteAccount(nil)},
		{"change password", HandleChangePassword(nil)},
		{"create token", HandleCreatePAT(nil)},
		{"list tokens", HandleListPATs(nil)},
		{"revoke token", HandleRevokePAT(nil)},
		{"accept invitation", RequirePersonalSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))},
	} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer eyJ.sso.jwt")
		req = req.WithContext(ContextWithClaims(context.Background(), sso))
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "single sign-on") {
			t.Errorf("%s: status %d body %s, want 403", c.name, rec.Code, rec.Body.String())
		}
	}
	for _, via := range []string{LoginViaPassword, LoginViaPasskey} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req = req.WithContext(ContextWithClaims(context.Background(), &Claims{Subject: "u1", LoginVia: via}))
		rec := httptest.NewRecorder()
		RequirePersonalSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Errorf("%s session refused: %d", via, rec.Code)
		}
	}
}

// Deny by default for a project-scoped token: only project routes, the
// project list and the profile.
func TestScopedTokenAllowed(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/platform/projects/p1/data/todos", true},
		{"POST", "/platform/projects/p1/data/sql", true},
		{"GET", "/v1/tenants", true},
		{"GET", "/platform/auth/account/profile", true},
		{"POST", "/v1/tenants", false},
		{"PATCH", "/v1/tenants/p1", false},
		{"PATCH", "/v1/tenants/p1/org", false},
		{"DELETE", "/v1/tenants/p1", false},
		{"PATCH", "/platform/auth/account/profile", false},
		{"GET", "/platform/auth/account/tokens", false},
		{"GET", "/platform/orgs", false},
		{"POST", "/platform/invitations/accept", false},
		{"GET", "/platform/billing/invoices", false},
		{"POST", "/platform/support", false},
		{"GET", "/platform/config/plans", false},
		{"GET", "/platform/admin/users", false},
		{"GET", "/platform/projectsX", false},
	} {
		if got := ScopedTokenAllowed(c.method, c.path); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
