package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// End-to-end: per-project cors_origins should let a tenant's browser app
// preflight succeed even when the global allowlist doesn't include the
// origin. Closes the gap reported by beta testers: their dev app at
// http://localhost:3000 can't talk to the prod gateway because localhost
// isn't in the platform-level allowlist.

func makeReq(method, origin string, ctx context.Context) *http.Request {
	r := httptest.NewRequest(method, "https://newtek2.eurobase.app/v1/auth/signup", nil)
	r.Header.Set("Origin", origin)
	if method == http.MethodOptions {
		r.Header.Set("Access-Control-Request-Method", "POST")
	}
	return r.WithContext(ctx)
}

func newPlatformGlobalCORS() func(http.Handler) http.Handler {
	// Mirrors the prod default: tenant subdomain wildcard + apex.
	return NewCORSMiddleware([]string{"https://*.eurobase.app", "https://eurobase.app"})
}

func ctxWithProjectCORS(origins ...string) context.Context {
	cfg := map[string]any{
		"providers":                  map[string]any{"email_password": map[string]any{"enabled": true}},
		"password_min_length":        8,
		"require_email_confirmation": false,
		"session_duration":           "168h",
		"redirect_urls":              []string{"http://localhost:3000"},
	}
	if len(origins) > 0 {
		cfg["cors_origins"] = origins
	}
	raw, _ := json.Marshal(cfg)
	pc := &auth.ProjectContext{
		ProjectID:  "p-test",
		SchemaName: "tenant_test",
		Slug:       "newtek2",
		AuthConfig: raw,
	}
	return auth.ContextWithProject(context.Background(), pc)
}

func TestCORS_PerProjectAllowsTenantConfiguredOrigin(t *testing.T) {
	cors := newPlatformGlobalCORS()
	ctx := ctxWithProjectCORS("http://localhost:3000")
	rr := httptest.NewRecorder()
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	handler.ServeHTTP(rr, makeReq(http.MethodOptions, "http://localhost:3000", ctx))

	got := rr.Header().Get("Access-Control-Allow-Origin")
	if got != "http://localhost:3000" {
		t.Errorf("preflight Access-Control-Allow-Origin = %q, want http://localhost:3000", got)
	}
	if rr.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rr.Code)
	}
}

func TestCORS_RejectsOriginNotInProjectAllowlist(t *testing.T) {
	cors := newPlatformGlobalCORS()
	ctx := ctxWithProjectCORS("http://localhost:3000")
	rr := httptest.NewRecorder()
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	handler.ServeHTTP(rr, makeReq(http.MethodOptions, "https://attacker.example.com", ctx))

	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight should NOT echo Access-Control-Allow-Origin for disallowed origin")
	}
}

func TestCORS_GlobalAllowlistStillWorks(t *testing.T) {
	cors := newPlatformGlobalCORS()
	// No project context → only global allowlist applies.
	rr := httptest.NewRecorder()
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	handler.ServeHTTP(rr, makeReq(http.MethodOptions, "https://newtek2.eurobase.app", context.Background()))

	if rr.Header().Get("Access-Control-Allow-Origin") != "https://newtek2.eurobase.app" {
		t.Errorf("global wildcard should match tenant subdomain origin")
	}
}

func TestCORS_GlobalRejectsLocalhostWithoutPerProjectConfig(t *testing.T) {
	cors := newPlatformGlobalCORS()
	rr := httptest.NewRecorder()
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	// No project context, localhost not in global allowlist.
	handler.ServeHTTP(rr, makeReq(http.MethodOptions, "http://localhost:3000", context.Background()))

	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("global allowlist should not include localhost in prod")
	}
}

func TestCORS_NoOriginHeaderPassesThrough(t *testing.T) {
	cors := newPlatformGlobalCORS()
	rr := httptest.NewRecorder()
	called := false
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	r := httptest.NewRequest(http.MethodGet, "https://newtek2.eurobase.app/health", nil)
	handler.ServeHTTP(rr, r)
	if !called {
		t.Error("non-CORS request should pass through to handler")
	}
}

func TestCORS_PreflightSetsAllowMethodsAndHeaders(t *testing.T) {
	cors := newPlatformGlobalCORS()
	ctx := ctxWithProjectCORS("http://localhost:3000")
	rr := httptest.NewRecorder()
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	handler.ServeHTTP(rr, makeReq(http.MethodOptions, "http://localhost:3000", ctx))
	if !contains(rr.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("preflight should advertise POST: got %q", rr.Header().Get("Access-Control-Allow-Methods"))
	}
	if !contains(rr.Header().Get("Access-Control-Allow-Headers"), "apikey") {
		t.Errorf("preflight should advertise apikey: got %q", rr.Header().Get("Access-Control-Allow-Headers"))
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
