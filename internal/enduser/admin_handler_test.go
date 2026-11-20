package enduser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/jackc/pgx/v5/pgxpool"
)

func adminRequest(method, path string, pc *auth.ProjectContext, header map[string]string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(`{"email":"a@example.com","password":"long-enough-1"}`))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	if pc != nil {
		r = r.WithContext(auth.ContextWithProject(r.Context(), pc))
	}
	return r
}

// The admin API answers only to the project's secret key: a public key is
// refused before any handler runs, with or without an end-user token.
func TestAdminRoutes_SecretKeyOnly(t *testing.T) {
	// A nil pool: any request that got past the guard would panic, so a
	// clean 403 / 401 proves no handler ran.
	h := AdminRoutes(nil, nil)
	routes := []struct{ method, path string }{
		{"GET", "/"},
		{"POST", "/"},
		{"GET", "/00000000-0000-0000-0000-000000000001"},
		{"PATCH", "/00000000-0000-0000-0000-000000000001"},
		{"DELETE", "/00000000-0000-0000-0000-000000000001"},
		{"POST", "/00000000-0000-0000-0000-000000000001/suspend"},
		{"DELETE", "/00000000-0000-0000-0000-000000000001/suspend"},
		{"POST", "/00000000-0000-0000-0000-000000000001/password"},
		{"POST", "/00000000-0000-0000-0000-000000000001/signout"},
	}
	for _, rt := range routes {
		for _, keyType := range []string{"public", "", "anon"} {
			pc := &auth.ProjectContext{ProjectID: "p", SchemaName: "tenant_x", KeyType: keyType}
			// An end-user token on the request changes nothing.
			req := adminRequest(rt.method, rt.path, pc, map[string]string{"Authorization": "Bearer eyJhbGciOiJIUzI1NiJ9.e30.x"})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "secret_key_required") {
				t.Errorf("%s %s key=%q: %d %s (want 403 secret_key_required)", rt.method, rt.path, keyType, rec.Code, rec.Body.String())
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, adminRequest(rt.method, rt.path, nil, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without project: %d (want 401)", rt.method, rt.path, rec.Code)
		}
	}
}

func TestRequireSecretKey_PassesSecret(t *testing.T) {
	reached := false
	h := RequireSecretKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/", &auth.ProjectContext{ProjectID: "p", SchemaName: "s", KeyType: "secret"}, map[string]string{"apikey": "eb_sk_x"}))
	if !reached {
		t.Fatalf("secret key refused: %d %s", rec.Code, rec.Body.String())
	}
}

// A secret key sent as ?apikey= (the API-key middleware's fallback for
// browser redirects) is refused: it would end up in URLs and logs.
func TestRequireSecretKey_RefusesKeyInURL(t *testing.T) {
	reached := false
	h := RequireSecretKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	rec := httptest.NewRecorder()
	req := adminRequest("GET", "/?apikey=eb_sk_x", &auth.ProjectContext{ProjectID: "p", SchemaName: "s", KeyType: "secret"}, nil)
	h.ServeHTTP(rec, req)
	if reached || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "apikey_header_required") {
		t.Fatalf("key in URL: reached=%v %d %s", reached, rec.Code, rec.Body.String())
	}
}

func TestEscapeILIKE(t *testing.T) {
	for in, want := range map[string]string{
		"ann":        "ann",
		"50%":        `50\%`,
		"a_b":        `a\_b`,
		`back\slash`: `back\\slash`,
	} {
		if got := escapeILIKE(in); got != want {
			t.Errorf("escapeILIKE(%q) = %q, want %q", in, got, want)
		}
	}
}

// Body and field size limits answer before any database access (nil pool).
func TestUserWriteLimits(t *testing.T) {
	h := AdminRoutes(nil, nil)
	pc := &auth.ProjectContext{ProjectID: "p", SchemaName: "s", KeyType: "secret"}
	send := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("apikey", "eb_sk_x")
		r = r.WithContext(auth.ContextWithProject(r.Context(), pc))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	id := "/00000000-0000-0000-0000-000000000001"
	big := strings.Repeat("x", 70<<10)
	if rec := send("POST", "/", `{"email":"a@example.com","display_name":"`+big+`"}`); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("create, 70 KiB body: %d", rec.Code)
	}
	if rec := send("POST", id+"/password", `{"password":"`+big+`"}`); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("password, 70 KiB body: %d", rec.Code)
	}
	if rec := send("PATCH", id, `{"metadata":{"a":"`+strings.Repeat("x", 17<<10)+`"}}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "16 KiB") {
		t.Errorf("update, 17 KiB metadata: %d %s", rec.Code, rec.Body.String())
	}
	if rec := send("POST", "/", `{"email":"a@example.com","display_name":"`+strings.Repeat("é", 257)+`"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "256") {
		t.Errorf("create, long display name: %d %s", rec.Code, rec.Body.String())
	}
}

// A project with a dedicated database is served from the pool on the
// request or refused — never the shared pool.
func TestAdminTenantContext_DedicatedFailsClosed(t *testing.T) {
	var gotSchema, gotProject, gotKey string
	reached := false
	h := adminTenantContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		gotSchema = query.SchemaFromContext(r.Context())
		gotProject = query.ProjectIDFromContext(r.Context())
		gotKey = query.KeyTypeFromContext(r.Context())
	}))

	// Flagged on the project context, no pool → 503.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminRequest("GET", "/", &auth.ProjectContext{ProjectID: "p", SchemaName: "s", KeyType: "secret", HasDedicatedDB: true}, nil))
	if reached || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("dedicated without pool: reached=%v %d", reached, rec.Code)
	}

	// Flagged on the query context only, no pool → 503.
	req := adminRequest("GET", "/", &auth.ProjectContext{ProjectID: "p", SchemaName: "s", KeyType: "secret"}, nil)
	req = req.WithContext(query.WithDedicatedDB(req.Context()))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if reached || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("dedicated marker without pool: reached=%v %d", reached, rec.Code)
	}

	// With a pool on the request it continues (the pool is never dialled).
	p, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	req = adminRequest("GET", "/", &auth.ProjectContext{ProjectID: "p", SchemaName: "tenant_s", KeyType: "secret", HasDedicatedDB: true}, nil)
	req = req.WithContext(query.ContextWithTenantPool(req.Context(), p))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !reached || gotSchema != "tenant_s" || gotProject != "p" || gotKey != "secret" {
		t.Fatalf("dedicated with pool: reached=%v schema=%q project=%q key=%q", reached, gotSchema, gotProject, gotKey)
	}
}

func TestAdminRoutes_NonUUIDUserIs404(t *testing.T) {
	h := AdminRoutes(nil, nil)
	for _, path := range []string{"/not-a-uuid", "/not-a-uuid/suspend", "/1;drop/password"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, adminRequest("POST", path, &auth.ProjectContext{ProjectID: "p", SchemaName: "s", KeyType: "secret"}, map[string]string{"apikey": "eb_sk_x"}))
		if path == "/not-a-uuid" {
			// POST /{id} isn't a route.
			if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
				t.Errorf("%s: %d", path, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s (want 404)", path, rec.Code, rec.Body.String())
		}
	}
}

// Without an audit service on the context the helper is a no-op; with a
// request that is neither a console session nor a secret key it records
// nothing (and must not panic).
func TestAuditUserAction_NoServiceNoPanic(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	auditUserAction(r, "end_user.created", "u", nil)
}
