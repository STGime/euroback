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

	"github.com/eurobase/euroback/internal/auth"
)

// ssoOffClaims returns a Claims struct representing a password-
// authenticated session for the given user. Existing ListProjects
// tests seeded before migration 000121 shipped assume the SSO flag
// defaults to false on orgs — a password session sees every org
// project. New sso_required tests below use ssoOnClaimsFor with the
// org id to represent an SSO-authenticated session.
func ssoOffClaims(userID string) *auth.Claims {
	return &auth.Claims{Subject: userID, LoginVia: auth.LoginViaPassword}
}

// passkeyClaims represents a console session minted by passkey sign-in
// or password → passkey step-up (#621). MFA-backed, but NOT SSO: it
// must be refused by sso_required orgs exactly like a password session.
func passkeyClaims(userID string) *auth.Claims {
	return &auth.Claims{Subject: userID, LoginVia: auth.LoginViaPasskey}
}

// ssoOnClaimsFor returns a Claims struct representing a session
// authed via SSO for the given org. Only satisfies sso_required for
// that specific org — SSO against a different org, or password, is
// deliberately refused.
func ssoOnClaimsFor(userID, orgID string) *auth.Claims {
	return &auth.Claims{Subject: userID, LoginVia: auth.LoginViaSSO, SsoOrgID: orgID}
}

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

// TestCreateProject_AutoAttachForInvitedAdmin asserts that a
// non-creator admin (invited into the org with role=admin) gets
// the same auto-attach treatment as the creator. Pins the widened
// SELECT that keys on org_members.role = 'admin' rather than on
// organizations.created_by alone.
func TestCreateProject_AutoAttachForInvitedAdmin(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-widen@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-widen@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice Widen Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}

	// Invite Bob as ADMIN — non-creator admin case.
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'admin', 'manual')`,
		aliceOrg.ID, bob,
	); err != nil {
		t.Fatalf("seed bob as admin: %v", err)
	}

	// Bob creates a project — should auto-attach to Alice's org.
	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, bob, "bob-widen@test.eurobase.local", CreateProjectRequest{
		Name: "Bob Widen Project", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("bob CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	if proj.OrgID == nil {
		t.Fatalf("expected non-creator admin auto-attach; got nil org_id")
	}
	if *proj.OrgID != aliceOrg.ID {
		t.Fatalf("expected org_id %s (alice's org), got %s", aliceOrg.ID, *proj.OrgID)
	}
}

// TestCreateProject_AutoAttachSkipsForMember asserts that a plain
// MEMBER (not admin) does NOT get auto-attach. Keeps the invariant
// that a member's project stays personal by default; they'd have to
// use PATCH /platform/projects/{id}/org to attach explicitly.
func TestCreateProject_AutoAttachSkipsForMember(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-mem@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-mem@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice Member-scope Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}

	// Invite Bob as MEMBER only.
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'member', 'manual')`,
		aliceOrg.ID, bob,
	); err != nil {
		t.Fatalf("seed bob as member: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, bob, "bob-mem@test.eurobase.local", CreateProjectRequest{
		Name: "Bob Member Project", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("bob CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	if proj.OrgID != nil {
		t.Fatalf("expected member-role project to stay personal (nil org_id); got %s", *proj.OrgID)
	}
}

// TestCreateProject_AutoAttachPrefersOwnOrg asserts that when a
// user is admin in TWO orgs (their own creation + one they were
// invited into), auto-attach picks THEIR own org deterministically.
// Pins the ORDER BY (o.created_by = caller) DESC tiebreak.
func TestCreateProject_AutoAttachPrefersOwnOrg(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-tie@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-tie@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)

	// Alice creates her org first.
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice Tie Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}
	// Bob creates HIS own org.
	bobOrg, err := orgsSvc.CreateOrg(ctx, bob, "Bob Tie Org")
	if err != nil {
		t.Fatalf("bob CreateOrg: %v", err)
	}
	// Then Bob is invited as admin into Alice's org too.
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'admin', 'manual')`,
		aliceOrg.ID, bob,
	); err != nil {
		t.Fatalf("seed bob as admin of alice's org: %v", err)
	}

	// Bob creates a project — should land under bob's own org, not
	// alice's, even though he's admin in both.
	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, bob, "bob-tie@test.eurobase.local", CreateProjectRequest{
		Name: "Bob Tie Project", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("bob CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	if proj.OrgID == nil {
		t.Fatalf("expected auto-attach; got nil")
	}
	if *proj.OrgID != bobOrg.ID {
		t.Fatalf("expected bob's own org %s (created_by tiebreak), got %s", bobOrg.ID, *proj.OrgID)
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
	// SSO enforcement is a no-op for this org (default sso_required=false),
	// so an empty-loginVia session (Bob logged in via password) is
	// treated as-if fully authorised.
	got, err := svc.ListProjects(ctx, ssoOffClaims(bob))
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

// TestCanDeleteProject_OrgAdminNonCreator asserts that a non-creator
// org admin can delete an org-owned project they didn't personally
// create — the pre-org-plumbing gate refused this (only project
// owner could delete), leaving a real operational gap when the
// creator left the org.
func TestCanDeleteProject_OrgAdminNonCreator(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-canadmin@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-canadmin@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice Delete Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}
	// Bob invited as admin (non-creator admin).
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'admin', 'manual')`,
		aliceOrg.ID, bob,
	); err != nil {
		t.Fatalf("seed bob as admin: %v", err)
	}

	// Alice creates the project (auto-attaches to her org).
	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, alice, "alice-canadmin@test.eurobase.local", CreateProjectRequest{
		Name: "Delete Target", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	// Bob (non-creator admin) should be authorised.
	canBob, err := svc.CanDeleteProject(ctx, proj.ID, bob)
	if err != nil {
		t.Fatalf("CanDeleteProject bob: %v", err)
	}
	if !canBob {
		t.Fatalf("expected non-creator admin bob to be authorised; got false")
	}

	// Alice (creator + owner) should also still be authorised.
	canAlice, err := svc.CanDeleteProject(ctx, proj.ID, alice)
	if err != nil {
		t.Fatalf("CanDeleteProject alice: %v", err)
	}
	if !canAlice {
		t.Fatalf("expected creator alice to still be authorised; got false")
	}
}

// TestCanDeleteProject_RejectsUnrelatedUser asserts a plain user
// with no project-membership and no org-membership can't delete an
// org-owned project. Keeps the fence tight.
func TestCanDeleteProject_RejectsUnrelatedUser(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-reject@test.eurobase.local")
	carol := insertTestPlatformUser(t, pool, "carol-reject@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, carol})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	if _, err := orgsSvc.CreateOrg(ctx, alice, "Alice Fence Org"); err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}
	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, alice, "alice-reject@test.eurobase.local", CreateProjectRequest{
		Name: "Fence Target", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	// Carol is neither a project member nor an org member — must be refused.
	can, err := svc.CanDeleteProject(ctx, proj.ID, carol)
	if err != nil {
		t.Fatalf("CanDeleteProject carol: %v", err)
	}
	if can {
		t.Fatalf("expected unrelated carol to be refused; got true")
	}
}

// TestCanDeleteProject_RejectsAdminOfDifferentOrg asserts that an
// admin of some OTHER org can't delete a project belonging to a
// different org — pins the cross-org boundary the JOIN provides.
// Without the join predicate `om.org_id = p.org_id`, a rogue admin
// could reach any org-owned project.
func TestCanDeleteProject_RejectsAdminOfDifferentOrg(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-crossorg@test.eurobase.local")
	eve := insertTestPlatformUser(t, pool, "eve-crossorg@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, eve})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	// Alice creates her own org + a project in it.
	if _, err := orgsSvc.CreateOrg(ctx, alice, "Alice Cross Org"); err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}
	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, alice, "alice-crossorg@test.eurobase.local", CreateProjectRequest{
		Name: "Cross Org Target", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	// Eve creates her OWN separate org (she's admin there, not
	// Alice's). She has no membership in Alice's org.
	if _, err := orgsSvc.CreateOrg(ctx, eve, "Eve Own Org"); err != nil {
		t.Fatalf("eve CreateOrg: %v", err)
	}

	// Eve, admin of her own org, should NOT be authorised to delete
	// Alice's project — the join predicate `om.org_id = p.org_id`
	// blocks the cross-org reach.
	can, err := svc.CanDeleteProject(ctx, proj.ID, eve)
	if err != nil {
		t.Fatalf("CanDeleteProject eve: %v", err)
	}
	if can {
		t.Fatalf("cross-org boundary broken: eve (admin of a different org) got authorised to delete alice's project")
	}
}

// TestCanDeleteProject_RejectsMemberOnly asserts that a MEMBER (not
// admin) of the org can't delete the project. Delete stays an
// admin-role action.
func TestCanDeleteProject_RejectsMemberOnly(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-memcheck@test.eurobase.local")
	dave := insertTestPlatformUser(t, pool, "dave-memcheck@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, dave})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice Memcheck Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}
	// Dave: MEMBER of the org, not admin.
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'member', 'manual')`,
		aliceOrg.ID, dave,
	); err != nil {
		t.Fatalf("seed dave as member: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, alice, "alice-memcheck@test.eurobase.local", CreateProjectRequest{
		Name: "Member Check Target", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	can, err := svc.CanDeleteProject(ctx, proj.ID, dave)
	if err != nil {
		t.Fatalf("CanDeleteProject dave: %v", err)
	}
	if can {
		t.Fatalf("expected org member (non-admin) dave to be refused; got true")
	}
}

// TestCreateProject_ExplicitPersonalForcesNoAttach asserts that
// OrgIDExplicit=true + OrgID=nil skips auto-attach even when the
// caller admins an org. The picker's "Personal" option.
func TestCreateProject_ExplicitPersonalForcesNoAttach(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	uid := insertTestPlatformUser(t, pool, "explicit-personal@test.eurobase.local")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = $1`, uid) })

	orgsSvc, _ := NewOrgsService(pool, nil)
	if _, err := orgsSvc.CreateOrg(ctx, uid, "Explicit Personal Org"); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, uid, "explicit-personal@test.eurobase.local", CreateProjectRequest{
		Name: "Explicit Personal Project", Region: "fr-par", Plan: "free",
		OrgIDExplicit: true, // present as null → force personal
		OrgID:         nil,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	if proj.OrgID != nil {
		t.Fatalf("expected personal (nil org_id) via explicit-null; got %s", *proj.OrgID)
	}
}

// TestCreateProject_ExplicitOrgIDAttachAdminOK asserts that
// OrgIDExplicit=true + OrgID=<uuid> attaches when the caller admins
// the target org. Mirrors the picker's "choose an org" branch.
func TestCreateProject_ExplicitOrgIDAttachAdminOK(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	uid := insertTestPlatformUser(t, pool, "explicit-attach@test.eurobase.local")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = $1`, uid) })

	orgsSvc, _ := NewOrgsService(pool, nil)
	org, err := orgsSvc.CreateOrg(ctx, uid, "Explicit Attach Org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, uid, "explicit-attach@test.eurobase.local", CreateProjectRequest{
		Name: "Explicit Attach Project", Region: "fr-par", Plan: "free",
		OrgIDExplicit: true,
		OrgID:         &org.ID,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	if proj.OrgID == nil || *proj.OrgID != org.ID {
		t.Fatalf("expected attach to org %s; got %+v", org.ID, proj.OrgID)
	}
}

// TestCreateProject_ExplicitOrgIDNonAdminRejected asserts a caller
// who isn't an admin of the target org gets ErrOrgAttachForbidden
// on explicit-attach. Keeps the picker honest against a hand-crafted
// request that names an org the caller doesn't admin.
func TestCreateProject_ExplicitOrgIDNonAdminRejected(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-nonadmin@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-nonadmin@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice NonAdmin Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}
	// Bob is NOT a member of Alice's org.

	svc := &TenantService{pool: pool, developerPool: pool}
	_, err = svc.CreateProject(ctx, bob, "bob-nonadmin@test.eurobase.local", CreateProjectRequest{
		Name: "Bob Bogus Attach", Region: "fr-par", Plan: "free",
		OrgIDExplicit: true,
		OrgID:         &aliceOrg.ID,
	})
	if !errors.Is(err, ErrOrgAttachForbidden) {
		t.Fatalf("expected ErrOrgAttachForbidden for non-admin explicit attach; got %v", err)
	}
}

// TestListProjects_SSORequired_HidesOrgProjectsFromPasswordSession
// asserts that when an org has sso_required=true, a password-
// authenticated session's ListProjects call does NOT include the
// org-owned projects. Pins the enforcement contract migration
// 000121 shipped.
func TestListProjects_SSORequired_HidesOrgProjectsFromPasswordSession(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-ssoreq@test.eurobase.local")
	bob := insertTestPlatformUser(t, pool, "bob-ssoreq@test.eurobase.local")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = ANY($1::uuid[])`,
			[]string{alice, bob})
	})

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice SSO-Required Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}
	aliceProj, err := svc.CreateProject(ctx, alice, "alice-ssoreq@test.eurobase.local", CreateProjectRequest{
		Name: "Alice SSO Project", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, aliceProj.ID)

	// Bob invited to the org.
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'member', 'manual')`,
		aliceOrg.ID, bob,
	); err != nil {
		t.Fatalf("seed bob as org member: %v", err)
	}

	// Baseline: with sso_required=false, Bob's password session sees
	// alice's org project.
	got, err := svc.ListProjects(ctx, ssoOffClaims(bob))
	if err != nil {
		t.Fatalf("bob ListProjects (sso off): %v", err)
	}
	baseline := false
	for _, p := range got {
		if p.ID == aliceProj.ID {
			baseline = true
		}
	}
	if !baseline {
		t.Fatalf("baseline: bob should see alice's org project when sso_required=false; got %d projects", len(got))
	}

	// Flip sso_required=true directly in the DB (bypassing the OIDC-
	// config-required guard on SetSSORequired, which we don't need to
	// exercise here). SetSSORequired's own tests cover the guard.
	if _, err := pool.Exec(ctx,
		`UPDATE public.organizations SET sso_required = true WHERE id = $1::uuid`,
		aliceOrg.ID,
	); err != nil {
		t.Fatalf("flip sso_required: %v", err)
	}

	// Bob's password session must NOT see the org project anymore.
	got, err = svc.ListProjects(ctx, ssoOffClaims(bob))
	if err != nil {
		t.Fatalf("bob ListProjects (sso on, password session): %v", err)
	}
	for _, p := range got {
		if p.ID == aliceProj.ID {
			t.Fatalf("password session should not see sso_required-org project; found %s in ListProjects", aliceProj.ID)
		}
	}

	// Bob's SSO session for this org SHOULD see it again.
	got, err = svc.ListProjects(ctx, ssoOnClaimsFor(bob, aliceOrg.ID))
	if err != nil {
		t.Fatalf("bob ListProjects (sso on, sso session): %v", err)
	}
	found := false
	for _, p := range got {
		if p.ID == aliceProj.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("sso session for the right org should see the project; got %d projects", len(got))
	}

	// Bob's SSO session for a DIFFERENT org must NOT satisfy — pins
	// the sso_org_id equality check.
	got, err = svc.ListProjects(ctx, ssoOnClaimsFor(bob, "00000000-0000-0000-0000-000000000000"))
	if err != nil {
		t.Fatalf("bob ListProjects (sso on, wrong org): %v", err)
	}
	for _, p := range got {
		if p.ID == aliceProj.ID {
			t.Fatalf("SSO session for a different org must not satisfy; found %s", aliceProj.ID)
		}
	}
}

// TestEnforceOrgSSOForProject asserts the project-scoped gate that
// the round-1 review flagged as missing — direct project members of
// an sso_required org must be refused when their session isn't
// SSO-backed for that org. Pins the qtune scenario: a departed
// employee with password + project_members row is no longer
// authorised to open project routes once SSO is required.
func TestEnforceOrgSSOForProject(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	alice := insertTestPlatformUser(t, pool, "alice-enforceproj@test.eurobase.local")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE created_by = $1`, alice) })

	orgsSvc, _ := NewOrgsService(pool, nil)
	aliceOrg, err := orgsSvc.CreateOrg(ctx, alice, "Alice Enforce Proj Org")
	if err != nil {
		t.Fatalf("alice CreateOrg: %v", err)
	}

	svc := &TenantService{pool: pool, developerPool: pool}
	proj, err := svc.CreateProject(ctx, alice, "alice-enforceproj@test.eurobase.local", CreateProjectRequest{
		Name: "Project Proj Enforce", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("alice CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)

	// Baseline: sso_required=false, password session passes.
	if err := EnforceOrgSSOForProject(ctx, pool, ssoOffClaims(alice), proj.ID); err != nil {
		t.Fatalf("baseline sso_required=false expected pass; got %v", err)
	}

	// Flip sso_required=true.
	if _, err := pool.Exec(ctx,
		`UPDATE public.organizations SET sso_required = true WHERE id = $1::uuid`,
		aliceOrg.ID,
	); err != nil {
		t.Fatalf("flip sso_required: %v", err)
	}

	// Password session refused now.
	if err := EnforceOrgSSOForProject(ctx, pool, ssoOffClaims(alice), proj.ID); !errors.Is(err, ErrSSORequiredForOrg) {
		t.Fatalf("expected ErrSSORequiredForOrg for password session; got %v", err)
	}

	// Passkey session refused too (#621): MFA at Eurobase does not
	// substitute for the org's IdP when the org requires SSO.
	if err := EnforceOrgSSOForProject(ctx, pool, passkeyClaims(alice), proj.ID); !errors.Is(err, ErrSSORequiredForOrg) {
		t.Fatalf("expected ErrSSORequiredForOrg for passkey session; got %v", err)
	}

	// SSO session for the RIGHT org passes.
	if err := EnforceOrgSSOForProject(ctx, pool, ssoOnClaimsFor(alice, aliceOrg.ID), proj.ID); err != nil {
		t.Fatalf("expected pass for sso session with matching org; got %v", err)
	}

	// SSO session for a DIFFERENT org refused (cross-org boundary).
	if err := EnforceOrgSSOForProject(ctx, pool, ssoOnClaimsFor(alice, "00000000-0000-0000-0000-000000000000"), proj.ID); !errors.Is(err, ErrSSORequiredForOrg) {
		t.Fatalf("expected ErrSSORequiredForOrg for SSO to different org; got %v", err)
	}
}

// TestEnforceOrgSSOForProject_PersonalProject asserts personal
// projects (org_id NULL) always pass — SSO enforcement is scoped
// to org-owned projects only, doesn't leak into personal-project
// access.
func TestEnforceOrgSSOForProject_PersonalProject(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	uid := insertTestPlatformUser(t, pool, "personal-enforceproj@test.eurobase.local")

	// Personal project — no org auto-attach because developer pool nil.
	svc := &TenantService{pool: pool}
	proj, err := svc.CreateProject(ctx, uid, "personal-enforceproj@test.eurobase.local", CreateProjectRequest{
		Name: "Personal Enforce Target", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	defer cleanupProject(t, pool, proj.ID)
	if proj.OrgID != nil {
		t.Fatalf("expected personal project; got org_id=%s", *proj.OrgID)
	}

	// Password session must pass — the project has no org, so no
	// enforcement applies.
	if err := EnforceOrgSSOForProject(ctx, pool, ssoOffClaims(uid), proj.ID); err != nil {
		t.Fatalf("personal project should always pass sso enforcement; got %v", err)
	}
	// Same for a passkey session (#621).
	if err := EnforceOrgSSOForProject(ctx, pool, passkeyClaims(uid), proj.ID); err != nil {
		t.Fatalf("personal project should pass for a passkey session; got %v", err)
	}
}

// TestSessionSatisfiesSSOFor pins the pure predicate — no DB needed.
func TestSessionSatisfiesSSOFor(t *testing.T) {
	cases := []struct {
		name        string
		loginVia    string
		ssoOrgID    string
		targetOrg   string
		ssoRequired bool
		want        bool
	}{
		{"sso_required=false: password session passes", "password", "", "org-A", false, true},
		{"sso_required=false: empty loginVia passes", "", "", "org-A", false, true},
		{"sso_required=true: password session refused", "password", "", "org-A", true, false},
		{"sso_required=true: empty loginVia refused", "", "", "org-A", true, false},
		{"sso_required=true: SSO for same org passes", "sso", "org-A", "org-A", true, true},
		{"sso_required=true: SSO for different org refused", "sso", "org-B", "org-A", true, false},
		{"sso_required=true: SSO with empty sso_org_id refused", "sso", "", "org-A", true, false},
		{"sso_required=false: passkey session passes", "passkey", "", "org-A", false, true},
		{"sso_required=true: passkey session refused", "passkey", "", "org-A", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SessionSatisfiesSSOFor(c.loginVia, c.ssoOrgID, c.targetOrg, c.ssoRequired)
			if got != c.want {
				t.Fatalf("want %v, got %v", c.want, got)
			}
		})
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

	got, err := svc.ListProjects(ctx, ssoOffClaims(bob))
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
