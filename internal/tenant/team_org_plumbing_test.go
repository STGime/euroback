package tenant

// Integration tests for the Team-org project-attachment plumbing:
//   * CreateOrg single-org guard (one org per user, first release).
//   * CreateProject auto-attach to the caller's org.
//   * SetProjectOrg attach/detach + membership check.
//   * ListProjects org-union (invited members see org-owned projects).
//
// Tests skip cleanly when no DATABASE_URL is reachable (mirrors the
// pattern in service_test.go).

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestCreateOrg_SingleOrgGuard asserts CreateOrg returns
// ErrOrgAlreadyExists on the second attempt by the same creator.
func TestCreateOrg_SingleOrgGuard(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	uid := insertTestPlatformUser(t, pool, "org-guard@test.eurobase.local")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = $1`, uid) })

	svc, err := NewOrgsService(pool, nil)
	if err != nil {
		t.Fatalf("NewOrgsService: %v", err)
	}

	// First org — allowed.
	if _, err := svc.CreateOrg(ctx, uid, "First Org"); err != nil {
		t.Fatalf("first CreateOrg failed unexpectedly: %v", err)
	}

	// Second org — must fail with the sentinel so the handler can
	// translate to HTTP 409.
	_, err = svc.CreateOrg(ctx, uid, "Second Org")
	if !errors.Is(err, ErrOrgAlreadyExists) {
		t.Fatalf("second CreateOrg: expected ErrOrgAlreadyExists, got %v", err)
	}
}

// TestCreateOrg_InvitedElsewhereStillAllowed asserts that being a
// MEMBER of another user's org does NOT block a user from creating
// their own org. The guard keys on created_by, not membership.
func TestCreateOrg_InvitedElsewhereStillAllowed(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-invite@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-invite@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	svc, err := NewOrgsService(pool, nil)
	if err != nil {
		t.Fatalf("NewOrgsService: %v", err)
	}

	aliceOrg, err := svc.CreateOrg(ctx, alice, "Alice Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}

	// Add Bob as a MEMBER of Alice's org — should NOT block Bob's
	// own CreateOrg later.
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'member', 'manual')`,
		aliceOrg.ID, bob,
	); err != nil {
		t.Fatalf("seed bob as member of alice org: %v", err)
	}

	if _, err := svc.CreateOrg(ctx, bob, "Bob Own Org"); err != nil {
		t.Fatalf("bob CreateOrg while already invited elsewhere: %v", err)
	}
}

// TestCreateProject_AutoAttachToOrg asserts that a project created
// by a user who owns an org lands with projects.org_id = that org.
func TestCreateProject_AutoAttachToOrg(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	uid := insertTestPlatformUser(t, pool, "attach@test.eurobase.local")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = $1`, uid) })

	orgsSvc, err := NewOrgsService(pool, nil)
	if err != nil {
		t.Fatalf("NewOrgsService: %v", err)
	}
	org, err := orgsSvc.CreateOrg(ctx, uid, "Attach Test Org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	// TenantService needs the developer pool wired to trigger
	// auto-attach — in tests the same pool serves both roles.
	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, uid, "attach@test.eurobase.local", CreateProjectRequest{
		Name:   "Attach Test",
		Region: "fr-par",
		Plan:   "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	if proj.OrgID == nil {
		t.Fatalf("expected auto-attach to set org_id; got NULL")
	}
	if *proj.OrgID != org.ID {
		t.Fatalf("expected org_id %s, got %s", org.ID, *proj.OrgID)
	}
}

// TestCreateProject_NoAutoAttachWhenNoOrg asserts a user without an
// org creates projects with org_id NULL (personal). Regression on
// the pre-#591 baseline.
func TestCreateProject_NoAutoAttachWhenNoOrg(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	uid := insertTestPlatformUser(t, pool, "no-org@test.eurobase.local")

	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, uid, "no-org@test.eurobase.local", CreateProjectRequest{
		Name:   "No Org",
		Region: "fr-par",
		Plan:   "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	if proj.OrgID != nil {
		t.Fatalf("expected no org_id, got %v", *proj.OrgID)
	}
}

// TestSetProjectOrg_AttachThenDetach asserts the happy path of
// PATCH /platform/projects/{id}/org: set org_id, then clear it.
func TestSetProjectOrg_AttachThenDetach(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	uid := insertTestPlatformUser(t, pool, "setorg@test.eurobase.local")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = $1`, uid) })

	orgsSvc, _ := NewOrgsService(pool, nil)
	org, err := orgsSvc.CreateOrg(ctx, uid, "Set Org Test")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}
	// Create a personal project first (skip auto-attach by leaving
	// developerPool nil during the create).
	svcNoAttach := &TenantService{pool: pool}
	proj, err := svcNoAttach.CreateProject(ctx, uid, "setorg@test.eurobase.local", CreateProjectRequest{
		Name:   "Set Org Project",
		Region: "fr-par",
		Plan:   "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)
	if proj.OrgID != nil {
		t.Fatalf("expected personal project, got org_id=%s", *proj.OrgID)
	}

	// Attach.
	if err := svc.SetProjectOrg(ctx, proj.ID, uid, &org.ID); err != nil {
		t.Fatalf("SetProjectOrg attach: %v", err)
	}
	got, _ := svc.GetProject(ctx, proj.ID)
	if got.OrgID == nil || *got.OrgID != org.ID {
		t.Fatalf("expected attached org_id %s, got %+v", org.ID, got.OrgID)
	}

	// Detach.
	if err := svc.SetProjectOrg(ctx, proj.ID, uid, nil); err != nil {
		t.Fatalf("SetProjectOrg detach: %v", err)
	}
	got, _ = svc.GetProject(ctx, proj.ID)
	if got.OrgID != nil {
		t.Fatalf("expected detached (nil), got %s", *got.OrgID)
	}
}

// TestSetProjectOrg_RejectsNonMember asserts you cannot attach a
// project to an org you are not a member of.
func TestSetProjectOrg_RejectsNonMember(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-attach@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-attach@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice Attach Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}

	// Bob has a project of his own; he is NOT a member of Alice's org.
	svc := &TenantService{pool: pool, developerPool: pool}
	bobProj, err := (&TenantService{pool: pool}).CreateProject(ctx, bob, "bob-attach@test.eurobase.local", CreateProjectRequest{
		Name:   "Bob Project",
		Region: "fr-par",
		Plan:   "free",
	})
	if err != nil {
		t.Fatalf("bob CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, bobProj.ID)

	err = svc.SetProjectOrg(ctx, bobProj.ID, bob, &aliceOrg.ID)
	if !errors.Is(err, ErrOrgAttachForbidden) {
		t.Fatalf("expected ErrOrgAttachForbidden, got %v", err)
	}
}

// TestListProjects_OrgUnion asserts an invited org member sees the
// org-owned projects on ListProjects even without a direct
// project_members row. The exact failure signature we're guarding
// against: post-#536 hotfix rollback where invitees see nothing.
func TestListProjects_OrgUnion(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-list@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-list@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice List Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}
	aliceProj, err := svc.CreateProject(ctx, alice, "alice-list@test.eurobase.local", CreateProjectRequest{
		Name:   "Alice Proj",
		Region: "fr-par",
		Plan:   "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, aliceProj.ID)

	// Bob is invited to the org — but NOT added to project_members.
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'member', 'manual')`,
		aliceOrg.ID, bob,
	); err != nil {
		t.Fatalf("seed bob as org member: %v", err)
	}

	// Bob's ListProjects must surface Alice's project via the union.
	got, err := svc.ListProjects(ctx, bob)
	if err != nil {
		// Guard against the CLAUDE.md-recorded 42501 regression.
		if strings.Contains(err.Error(), "42501") || strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("42501 regression: %v", err)
		}
		t.Fatalf("bob ListProjects: %v", err)
	}
	found := false
	for _, p := range got {
		if p.ID == aliceProj.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected bob to see alice's org-owned project via org_members union; got %d projects, none matching", len(got))
	}
}

// TestListProjects_MergedOrderGlobalNewestFirst asserts that when
// results come from BOTH the direct-member branch and the org
// branch, the merged list is globally sorted newest-first — not two
// separate sorted runs concatenated. Pins the fix for the round-1
// review 🟢 note.
func TestListProjects_MergedOrderGlobalNewestFirst(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-order@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-order@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice Order Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}

	// Step 1: alice creates an OLD project attached to her org.
	oldOrgProj, err := svc.CreateProject(ctx, alice, "alice-order@test.eurobase.local", CreateProjectRequest{
		Name: "Old Org Project", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject (org): %v", err)
	}
	defer cleanupProject(t, pool, oldOrgProj.ID)

	// Force old timestamp on the org-owned project so it's clearly
	// older than bob's direct-member project below.
	if _, err := pool.Exec(ctx,
		`UPDATE projects SET created_at = now() - interval '1 day' WHERE id = $1::uuid`,
		oldOrgProj.ID,
	); err != nil {
		t.Fatalf("backdate old org project: %v", err)
	}

	// Step 2: bob is a direct member of a fresh project (via
	// project_members), and also an org member of Alice's org.
	// Without global sort, bob's ListProjects would concatenate
	// [bob's fresh direct proj] + [alice's old org proj] — that
	// order is correct by luck here. So we build the opposite:
	// alice creates a NEW org project (bob sees via union), and
	// bob has a directly-owned OLDER project. Correct newest-first
	// order must put alice's new org proj FIRST.
	bobOld, err := (&TenantService{pool: pool}).CreateProject(ctx, bob, "bob-order@test.eurobase.local", CreateProjectRequest{
		Name: "Bob Old Direct Project", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("bob CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, bobOld.ID)
	if _, err := pool.Exec(ctx,
		`UPDATE projects SET created_at = now() - interval '2 days' WHERE id = $1::uuid`,
		bobOld.ID,
	); err != nil {
		t.Fatalf("backdate bob direct project: %v", err)
	}

	newOrgProj, err := svc.CreateProject(ctx, alice, "alice-order@test.eurobase.local", CreateProjectRequest{
		Name: "New Org Project", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject (new org): %v", err)
	}
	defer cleanupProject(t, pool, newOrgProj.ID)
	// newOrgProj.CreatedAt = now(), which is newer than bob's old
	// direct project.

	// Bob is invited to the org (no direct project_members row for
	// alice's org-owned projects).
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'member', 'manual')
		 ON CONFLICT DO NOTHING`,
		aliceOrg.ID, bob,
	); err != nil {
		t.Fatalf("seed bob as org member: %v", err)
	}

	got, err := svc.ListProjects(ctx, bob)
	if err != nil {
		t.Fatalf("bob ListProjects: %v", err)
	}

	// Assert the merged list is globally newest-first: verify the
	// timestamps are monotonically non-increasing.
	for i := 1; i < len(got); i++ {
		if got[i-1].CreatedAt.Before(got[i].CreatedAt) {
			t.Fatalf("merged list not globally newest-first: %s (%s) before %s (%s)",
				got[i-1].Name, got[i-1].CreatedAt, got[i].Name, got[i].CreatedAt)
		}
	}
}
