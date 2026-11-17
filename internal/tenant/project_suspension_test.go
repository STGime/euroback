package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/eurobase/euroback/internal/auth"
)

// TestAdminSuspendProject_DB: a suspended project's API key stops working,
// suspending twice or unsuspending an active project is a 409, and
// unsuspending brings the key back.
func TestAdminSuspendProject_DB(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	svc := &TenantService{pool: pool}

	uid := insertTestPlatformUser(t, pool, "suspend-project@test.eurobase.local")
	project, err := svc.CreateProject(ctx, uid, "suspend-project@test.eurobase.local", CreateProjectRequest{
		Name: "Suspend Me", Slug: "test-suspend-project", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	t.Cleanup(func() { cleanupProject(t, pool, project.ID) })
	if _, err := pool.Exec(ctx, `UPDATE projects SET status = 'active' WHERE id = $1`, project.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}

	call := func(h http.HandlerFunc) int {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", project.ID)
		req := httptest.NewRequest(http.MethodPost, "/platform/admin/projects/"+project.ID+"/suspend", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	mw := auth.NewAPIKeyMiddleware(pool).Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	keyStatus := func() int {
		req := httptest.NewRequest(http.MethodGet, "/v1/db/todos", nil)
		req.Header.Set("apikey", project.PublicKey)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		return rec.Code
	}

	if c := keyStatus(); c != http.StatusNoContent {
		t.Fatalf("key on an active project: %d", c)
	}
	if c := call(AdminSuspendProject(pool)); c != http.StatusOK {
		t.Fatalf("suspend: %d", c)
	}
	if c := keyStatus(); c == http.StatusNoContent {
		t.Fatal("key still works on a suspended project")
	}
	if c := call(AdminSuspendProject(pool)); c != http.StatusConflict {
		t.Fatalf("second suspend: %d, want 409", c)
	}
	if c := call(AdminUnsuspendProject(pool)); c != http.StatusOK {
		t.Fatalf("unsuspend: %d", c)
	}
	if c := call(AdminUnsuspendProject(pool)); c != http.StatusConflict {
		t.Fatalf("unsuspend an active project: %d, want 409", c)
	}
	if c := keyStatus(); c != http.StatusNoContent {
		t.Fatalf("key after unsuspend: %d", c)
	}
}
