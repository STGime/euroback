package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// Org membership needs the invitee's consent: an invite is pending and
// grants nothing (no membership, so no org project access and no SSO
// sign-in) until the invitee — and only the invitee — accepts.
func TestOrgInvitation_NeedsInviteeConsent(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	adminID := insertTestPlatformUser(t, pool, "consent-admin@test.eurobase.local")
	inviteeID := insertTestPlatformUser(t, pool, "consent-invitee@test.eurobase.local")
	otherID := insertTestPlatformUser(t, pool, "consent-other@test.eurobase.local")

	svc, err := NewOrgsService(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	org, err := svc.CreateOrg(ctx, adminID, "Consent fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE id = $1::uuid`, org.ID) })

	inv, err := svc.InviteMember(ctx, org.ID, adminID, "Consent-Invitee@test.eurobase.local ", RoleOrgAdmin)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	// Pending: not a member — no SSO sign-in, no org access.
	if err := svc.EnsureMemberFromSSO(ctx, org.ID, inviteeID); !errors.Is(err, ErrOrgMemberMissing) {
		t.Fatalf("pending invitee counts as member for SSO: %v", err)
	}
	if _, _, err := svc.GetOrgForMember(ctx, inviteeID, org.ID); err == nil {
		t.Fatal("pending invitee can open the org")
	}
	if list, _ := svc.ListInvitationsForUser(ctx, inviteeID); len(list) != 1 || list[0].ID != inv.ID || list[0].OrgName != "Consent fixture" {
		t.Fatalf("invitee's pending list = %+v", list)
	}
	if list, _ := svc.ListPendingInvitations(ctx, org.ID); len(list) != 1 {
		t.Fatalf("org's pending list = %+v", list)
	}
	// Duplicates are refused.
	if _, err := svc.InviteMember(ctx, org.ID, adminID, "consent-invitee@test.eurobase.local", RoleOrgMember); !errors.Is(err, ErrInvitationExists) {
		t.Fatalf("second invite: %v", err)
	}

	// A malformed id is "not found", not an error.
	if _, err := svc.AcceptInvitation(ctx, inviteeID, "not-a-uuid"); !errors.Is(err, ErrInvitationNotFound) {
		t.Fatalf("malformed id: %v", err)
	}

	// Nobody else can accept or decline it — not another user, not the admin.
	for _, who := range []string{otherID, adminID} {
		if _, err := svc.AcceptInvitation(ctx, who, inv.ID); !errors.Is(err, ErrInvitationNotFound) {
			t.Fatalf("accept by %s: %v", who, err)
		}
		if err := svc.DeclineInvitation(ctx, who, inv.ID); !errors.Is(err, ErrInvitationNotFound) {
			t.Fatalf("decline by %s: %v", who, err)
		}
	}

	// The invitee accepts: now a member, with the invited role; invitation gone.
	m, err := svc.AcceptInvitation(ctx, inviteeID, inv.ID)
	if err != nil || m.Role != RoleOrgAdmin {
		t.Fatalf("accept: %+v, %v", m, err)
	}
	if err := svc.EnsureMemberFromSSO(ctx, org.ID, inviteeID); err != nil {
		t.Fatalf("accepted invitee not a member: %v", err)
	}
	if _, err := svc.AcceptInvitation(ctx, inviteeID, inv.ID); !errors.Is(err, ErrInvitationNotFound) {
		t.Fatalf("accepting twice: %v", err)
	}
	if _, err := svc.InviteMember(ctx, org.ID, adminID, "consent-invitee@test.eurobase.local", RoleOrgMember); !errors.Is(err, ErrMemberExists) {
		t.Fatalf("inviting a member: %v", err)
	}

	// Decline and revoke leave no membership.
	inv2, err := svc.InviteMember(ctx, org.ID, adminID, "consent-other@test.eurobase.local", RoleOrgMember)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeclineInvitation(ctx, otherID, inv2.ID); err != nil {
		t.Fatalf("decline: %v", err)
	}
	inv3, err := svc.InviteMember(ctx, org.ID, adminID, "consent-other@test.eurobase.local", RoleOrgMember)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeInvitation(ctx, org.ID, inv3.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := svc.EnsureMemberFromSSO(ctx, org.ID, otherID); !errors.Is(err, ErrOrgMemberMissing) {
		t.Fatalf("declined / revoked user is a member: %v", err)
	}

	// An expired invitation can't be accepted and isn't listed; a new
	// invite replaces it.
	inv4, err := svc.InviteMember(ctx, org.ID, adminID, "consent-other@test.eurobase.local", RoleOrgMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.org_invitations SET expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, inv4.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptInvitation(ctx, otherID, inv4.ID); !errors.Is(err, ErrInvitationNotFound) {
		t.Fatalf("accepting an expired invitation: %v", err)
	}
	if list, _ := svc.ListInvitationsForUser(ctx, otherID); len(list) != 0 {
		t.Fatalf("expired invitation listed: %+v", list)
	}
	if _, err := svc.InviteMember(ctx, org.ID, adminID, "consent-other@test.eurobase.local", RoleOrgMember); err != nil {
		t.Fatalf("re-invite after expiry: %v", err)
	}
	// Revoke is keyed to the org: another org can't delete this org's invitation.
	org2, err := svc.CreateOrg(ctx, otherID, "Consent fixture 2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE id = $1::uuid`, org2.ID) })
	pending, _ := svc.ListPendingInvitations(ctx, org.ID)
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	if err := svc.RevokeInvitation(ctx, org2.ID, pending[0].ID); !errors.Is(err, ErrInvitationNotFound) {
		t.Fatalf("cross-org revoke: %v", err)
	}
}

// An SSO session works only for its own org: that org's projects and
// settings, never the person's other projects or other orgs.
func TestSSOSessionScopedToItsOrg(t *testing.T) {
	sso := &auth.Claims{Subject: "u1", LoginVia: auth.LoginViaSSO, SsoOrgID: "org-a"}
	if !SSOSessionInScope(sso, "org-a") || SSOSessionInScope(sso, "org-b") || SSOSessionInScope(sso, "") {
		t.Fatal("SSO session scope wrong")
	}
	for _, via := range []string{auth.LoginViaPassword, auth.LoginViaPasskey, auth.LoginViaPAT, ""} {
		c := &auth.Claims{Subject: "u1", LoginVia: via}
		if !SSOSessionInScope(c, "") || !SSOSessionInScope(c, "org-b") {
			t.Fatalf("%q session limited", via)
		}
	}

	pool := setupTestDB(t)
	ctx := context.Background()
	userID := insertTestPlatformUser(t, pool, "sso-scope@test.eurobase.local")
	svc := &TenantService{pool: pool, developerPool: pool}
	personal, err := svc.CreateProject(ctx, userID, "sso-scope@test.eurobase.local", CreateProjectRequest{Name: "Personal", Slug: "test-sso-scope-personal", Region: "fr-par", Plan: "free"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupProject(t, pool, personal.ID) })
	orgProj, err := svc.CreateProject(ctx, userID, "sso-scope@test.eurobase.local", CreateProjectRequest{Name: "Org", Slug: "test-sso-scope-org", Region: "fr-par", Plan: "free"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupProject(t, pool, orgProj.ID) })
	orgs, _ := NewOrgsService(pool, nil)
	org, err := orgs.CreateOrg(ctx, userID, "SSO scope fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM public.organizations WHERE id = $1::uuid`, org.ID) })
	if _, err := pool.Exec(ctx, `UPDATE projects SET org_id = $1 WHERE id = $2`, org.ID, orgProj.ID); err != nil {
		t.Fatal(err)
	}

	session := &auth.Claims{Subject: userID, Email: "sso-scope@test.eurobase.local", LoginVia: auth.LoginViaSSO, SsoOrgID: org.ID}
	if err := EnforceOrgSSOForProject(ctx, pool, session, orgProj.ID); err != nil {
		t.Fatalf("SSO session refused on its org's project: %v", err)
	}
	if err := EnforceOrgSSOForProject(ctx, pool, session, personal.ID); !errors.Is(err, ErrSSOSessionOutOfScope) {
		t.Fatalf("SSO session reached a personal project: %v", err)
	}
	password := &auth.Claims{Subject: userID, LoginVia: auth.LoginViaPassword}
	if err := EnforceOrgSSOForProject(ctx, pool, password, personal.ID); err != nil {
		t.Fatalf("password session refused: %v", err)
	}
	list, err := svc.ListProjects(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != orgProj.ID {
		t.Fatalf("SSO session lists %d projects (%+v), want only the org's", len(list), list)
	}
	if list, _ := svc.ListProjects(ctx, password); len(list) != 2 {
		t.Fatalf("password session lists %d projects, want 2", len(list))
	}
}
