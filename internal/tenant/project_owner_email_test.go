package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// The project list names the owner only on projects the caller doesn't own.
func TestListProjects_OwnerEmail(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	ownerEmail := "list-owner@test.eurobase.local"
	owner := insertTestPlatformUser(t, pool, ownerEmail)
	member := insertTestPlatformUser(t, pool, "list-member@test.eurobase.local")
	svc := &TenantService{pool: pool, developerPool: pool}

	p, err := svc.CreateProject(ctx, owner, ownerEmail, CreateProjectRequest{
		Name: "owner-email-list", Slug: "owner-email-list", Region: "fr-par", Plan: "free", OrgIDExplicit: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { cleanupProject(t, pool, p.ID) })
	if _, err := pool.Exec(ctx,
		`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'developer')`, p.ID, member,
	); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// Through the HTTP handler: it maps projects into its own response
	// type, which once dropped the field.
	handler := HandleListProjects(pool, svc)
	find := func(uid, email string) *ProjectListItem {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/tenants", nil)
		req = req.WithContext(auth.ContextWithClaims(ctx, &auth.Claims{Subject: uid, Email: email, LoginVia: auth.LoginViaPassword}))
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list projects: status %d: %s", rec.Code, rec.Body.String())
		}
		var list []ProjectListItem
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		for i := range list {
			if list[i].ID == p.ID {
				return &list[i]
			}
		}
		t.Fatalf("project missing from %s's list", email)
		return nil
	}

	if got := find(owner, ownerEmail); got.OwnerEmail != nil {
		t.Errorf("owner's own listing has owner_email = %q, want none", *got.OwnerEmail)
	}
	got := find(member, "list-member@test.eurobase.local")
	if got.OwnerEmail == nil || *got.OwnerEmail != ownerEmail {
		t.Errorf("member's listing owner_email = %v, want %q", got.OwnerEmail, ownerEmail)
	}
}
