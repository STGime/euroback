package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
)

// PATCH /v1/tenants/{id}/org and DELETE /v1/tenants/{id} are outside the
// project middleware: StashCallerRole must put the caller's role on the
// request (RequireRole has no fallback). Routes wired as in router.go.
func TestStashCallerRole_Routes(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	owner := insertTestPlatformUser(t, pool, "stash-owner@test.eurobase.local")
	member := insertTestPlatformUser(t, pool, "stash-member@test.eurobase.local")
	stranger := insertTestPlatformUser(t, pool, "stash-stranger@test.eurobase.local")
	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, owner, "stash-owner@test.eurobase.local", CreateProjectRequest{Name: "stash", Slug: "test-stash-role", Region: "fr-par", Plan: "free", OrgIDExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupProject(t, pool, proj.ID) })
	if _, err := pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'admin')`, proj.ID, member); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			uid := req.Header.Get("X-Test-User")
			next.ServeHTTP(w, req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: uid, LoginVia: auth.LoginViaPassword})))
		})
	})
	r.With(StashCallerRole(pool, pool)).Patch("/v1/tenants/{id}/org", HandleSetProjectOrg(pool, svc))
	r.With(StashCallerRole(pool, pool)).Delete("/v1/tenants/{id}", HandleDeleteProject(pool, svc))
	do := func(method, path, uid, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Test-User", uid)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := do("PATCH", "/v1/tenants/"+proj.ID+"/org", stranger, `{"org_id":null}`); code != http.StatusNotFound {
		t.Errorf("stranger move: %d, want 404", code)
	}
	if code := do("PATCH", "/v1/tenants/"+proj.ID+"/org", member, `{"org_id":null}`); code != http.StatusForbidden {
		t.Errorf("admin member move (owner only): %d, want 403", code)
	}
	if code := do("PATCH", "/v1/tenants/"+proj.ID+"/org", owner, `{"org_id":null}`); code != http.StatusOK {
		t.Errorf("owner move: %d, want 200", code)
	}
	if code := do("DELETE", "/v1/tenants/"+proj.ID, stranger, ""); code != http.StatusNotFound {
		t.Errorf("stranger delete: %d, want 404", code)
	}
	if code := do("DELETE", "/v1/tenants/"+proj.ID, member, ""); code != http.StatusForbidden {
		t.Errorf("project admin delete: %d, want 403", code)
	}
}
