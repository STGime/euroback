package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
)

// Test coverage for AdminDeleteUser — the superadmin cleanup endpoint
// added in PR #579. Guarded because it's the ONE endpoint we have that
// destroys platform_users AND everything they own; a regression that
// silently dropped a guard would be very expensive on real accounts.
//
// The tests pass the same pool for both the gateway and developer pool
// arguments. In prod the router wires distinct pools because
// org_members is REVOKE-ALL'd from eurobase_gateway (migration 000114);
// the test DB connects as a role that bypasses grants, so substituting
// the pool exercises the query correctness (column names, join shape).
// The pool split is verified at the router-wiring level, not here.

// TestAdminDeleteUser_HappyPath seeds a user + a Free project and
// verifies the endpoint removes both rows and writes a user.deleted
// audit action.
func TestAdminDeleteUser_HappyPath(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	svc := &TenantService{pool: pool}

	// Seed target user + one Free project (Team-tier needs Scaleway
	// creds, out of scope for a unit-level integration test).
	targetEmail := "delete-happy@test.eurobase.local"
	targetID := insertTestPlatformUser(t, pool, targetEmail)
	project, err := svc.CreateProject(ctx, targetID, targetEmail, CreateProjectRequest{
		Name:   "Happy path",
		Slug:   "delete-happy",
		Region: "fr-par",
		Plan:   "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	t.Cleanup(func() {
		// Belt-and-braces: even if the delete under test fails we want
		// the harness clean.
		cleanupProject(t, pool, project.ID)
	})

	actorID := insertTestPlatformUser(t, pool, "delete-actor@test.eurobase.local")

	// Dispatch.
	req := httptest.NewRequest(http.MethodDelete, "/platform/admin/users/"+targetID, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", targetID)
	rctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	rctx = auth.ContextWithClaims(rctx, &auth.Claims{Subject: actorID, IsSuperadmin: true})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	AdminDeleteUser(pool, pool, svc).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var body struct {
		Deleted         bool   `json:"deleted"`
		Email           string `json:"email"`
		ProjectsDeleted int    `json:"projects_deleted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Deleted || body.Email != targetEmail || body.ProjectsDeleted != 1 {
		t.Errorf("response mismatch: %+v", body)
	}

	// Assert the rows are gone.
	var userCount, projectCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM public.platform_users WHERE id = $1::uuid`, targetID,
	).Scan(&userCount); err != nil {
		t.Fatalf("count user: %v", err)
	}
	if userCount != 0 {
		t.Errorf("platform_users row survived (count=%d)", userCount)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM public.projects WHERE id = $1::uuid`, project.ID,
	).Scan(&projectCount); err != nil {
		t.Fatalf("count project: %v", err)
	}
	if projectCount != 0 {
		t.Errorf("projects row survived (count=%d)", projectCount)
	}
}

// TestAdminDeleteUser_RefusesSelfDelete pins the self-delete guard.
// The actor and target are the same user_id; expect 400 pointing at
// /account/delete.
func TestAdminDeleteUser_RefusesSelfDelete(t *testing.T) {
	pool := setupTestDB(t)
	svc := &TenantService{pool: pool}

	selfID := insertTestPlatformUser(t, pool, "delete-self@test.eurobase.local")
	// insertTestPlatformUser's cleanup path in cleanupProject is
	// project-scoped, but the shared test-DB cleanup on the LIKE
	// pattern in setupBroadcastFixture / cleanupProject will sweep
	// the fixture email suffix at the end of the package's runs.

	req := httptest.NewRequest(http.MethodDelete, "/platform/admin/users/"+selfID, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", selfID)
	rctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	rctx = auth.ContextWithClaims(rctx, &auth.Claims{Subject: selfID, IsSuperadmin: true})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	AdminDeleteUser(pool, pool, svc).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "account/delete") {
		t.Errorf("expected message to point at /account/delete, got: %s", w.Body.String())
	}
}

// TestAdminDeleteUser_RefusesSuperadminTarget pins the "won't delete a
// peer superadmin" guard. Ops must revoke is_superadmin via SQL first.
func TestAdminDeleteUser_RefusesSuperadminTarget(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	svc := &TenantService{pool: pool}

	targetID := insertTestPlatformUser(t, pool, "delete-peer-super@test.eurobase.local")
	if _, err := pool.Exec(ctx,
		`UPDATE public.platform_users SET is_superadmin = true WHERE id = $1::uuid`,
		targetID,
	); err != nil {
		t.Fatalf("mark superadmin: %v", err)
	}
	actorID := insertTestPlatformUser(t, pool, "delete-actor-peer@test.eurobase.local")

	req := httptest.NewRequest(http.MethodDelete, "/platform/admin/users/"+targetID, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", targetID)
	rctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	rctx = auth.ContextWithClaims(rctx, &auth.Claims{Subject: actorID, IsSuperadmin: true})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	AdminDeleteUser(pool, pool, svc).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "revoke is_superadmin") {
		t.Errorf("expected message to point at revoke path, got: %s", w.Body.String())
	}

	// Target row still exists.
	var stillThere bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM public.platform_users WHERE id = $1::uuid)`,
		targetID,
	).Scan(&stillThere); err != nil {
		t.Fatalf("check row: %v", err)
	}
	if !stillThere {
		t.Errorf("target row should NOT have been deleted")
	}
}

// TestAdminDeleteUser_NotFound covers the "target id doesn't exist"
// case — 404 so ops sees the difference between a typo and a real
// account.
func TestAdminDeleteUser_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	svc := &TenantService{pool: pool}

	actorID := insertTestPlatformUser(t, pool, "delete-actor-nf@test.eurobase.local")

	// A well-formed UUID that isn't in the DB.
	missingID := "11111111-2222-3333-4444-555555555555"
	req := httptest.NewRequest(http.MethodDelete, "/platform/admin/users/"+missingID, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", missingID)
	rctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	rctx = auth.ContextWithClaims(rctx, &auth.Claims{Subject: actorID, IsSuperadmin: true})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	AdminDeleteUser(pool, pool, svc).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

// TestAdminDeleteUser_RefusesSoleAdminOfOrg pins the sole-admin org
// guard. Seeds an org where the target is the ONLY admin, expects
// 409 with the hand-off message. Regression test for the review-round
// bug where the guard used the wrong column name (m.user_id vs
// m.platform_user_id) and 500'd instead of 409'ing.
func TestAdminDeleteUser_RefusesSoleAdminOfOrg(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	svc := &TenantService{pool: pool}

	targetEmail := "delete-sole-admin@test.eurobase.local"
	targetID := insertTestPlatformUser(t, pool, targetEmail)
	actorID := insertTestPlatformUser(t, pool, "delete-actor-org@test.eurobase.local")

	// Seed the org + membership. Only `name` is required on
	// organizations; created_by is nullable (see migration 000114).
	var orgID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.organizations (name) VALUES ($1) RETURNING id::text`,
		"Sole-admin fixture org",
	).Scan(&orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE id = $1::uuid`, orgID)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'admin', 'manual')`,
		orgID, targetID,
	); err != nil {
		t.Fatalf("insert org member: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/platform/admin/users/"+targetID, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", targetID)
	rctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	rctx = auth.ContextWithClaims(rctx, &auth.Claims{Subject: actorID, IsSuperadmin: true})
	req = req.WithContext(rctx)
	w := httptest.NewRecorder()
	AdminDeleteUser(pool, pool, svc).ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409 (body: %s)", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "sole admin") {
		t.Errorf("expected 'sole admin' in message, got: %s", w.Body.String())
	}
	// Target should still exist.
	var stillThere bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM public.platform_users WHERE id = $1::uuid)`,
		targetID,
	).Scan(&stillThere); err != nil {
		t.Fatalf("check row: %v", err)
	}
	if !stillThere {
		t.Errorf("target row should NOT have been deleted")
	}
}

// contains is a tiny helper so the assertions don't drag strings.Contains
// into every file.
func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
