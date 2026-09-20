package tenant

// Project-access resolution — additive helper introduced with the
// Team-tier SSO / orgs work. Callers that need to serve org members
// (project list, project detail, dashboard queries) opt into this
// helper; the ~35 sites that key on `projects.owner_id` today stay
// as-is per the additive plan.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrProjectNotFound is returned when the caller asked about a
// project id that doesn't exist. Distinguished from "exists but not
// accessible" so the caller can route to 404 vs 403.
var ErrProjectNotFound = pgx.ErrNoRows

// ProjectAccess describes how a user reaches a project + the effective
// project-level role that access implies.
type ProjectAccess struct {
	Accessible bool
	// ViaOwner is true when the user matches projects.owner_id
	// (the pre-org path — every existing project resolves this way).
	ViaOwner bool
	// ViaMember is true when the user has a project_members row.
	// The pre-org path (chapter 19 "Team Collaboration" invites).
	ViaMember bool
	// ViaOrg is true when the user matches an org_members row for
	// the project's org_id. Mutually non-exclusive with ViaOwner /
	// ViaMember — an owner who is also an org admin resolves all
	// applicable paths.
	ViaOrg bool
	// MemberRole is the caller's role from project_members, when
	// ViaMember is true ('viewer' | 'developer' | 'admin' | 'owner').
	// Empty otherwise.
	MemberRole string
	// OrgRole is the caller's role in the project's org, when
	// applicable ('admin' | 'member'). Empty when the project is
	// per-user (org_id NULL) or the user isn't in the org.
	OrgRole string
	// EffectiveRole is the highest project role the caller has via
	// any path — see mapOrgRoleToProjectRole for the org→project
	// mapping. Callers use this for RequireMinRole gating so a
	// single access lookup covers direct + org paths.
	EffectiveRole string
}

// mapOrgRoleToProjectRole projects an org membership onto a project
// role for access gating. Product decisions baked in:
//
//   - Org **admin** → project 'admin'. Mirrors SetProjectOrg's
//     auth check (org admins can attach/detach org projects), so
//     "admin of the org that owns this project" ≈ "admin of the
//     project" is the least-surprise mapping.
//   - Org **member** → project 'viewer' (deliberately conservative
//     default). RequireMinRole("developer") gates a lot of
//     dangerous edges — arbitrary /sql & /sql/transaction, DDL
//     endpoints, function deploy/update (incl. the RLS-bypass
//     allow_service_role flag), storage upload/delete, migrations,
//     webhooks, cron. Automatically granting all of that to every
//     org member on every org project by mere org attachment would
//     be a much larger surface than the least-privilege intent of
//     the whole SSO/org series. An org admin who wants a specific
//     member to have write access can promote them per-project via
//     the project's Members tab (chapter 19), which is the audit
//     trail we want anyway. If the "viewer only" default proves too
//     restrictive in practice we can widen — the reverse (narrowing
//     after users depend on writes) is much harder.
//   - Anything else → "" (no access via org).
//
// Not calling this "roleMap" so grep doesn't confuse it with the
// hierarchy in members.go.
func mapOrgRoleToProjectRole(orgRole string) string {
	switch orgRole {
	case RoleOrgAdmin:
		return "admin"
	case RoleOrgMember:
		return "viewer"
	default:
		return ""
	}
}

// higherRole returns the more-privileged of two project roles using
// the hierarchy in members.go (viewer < developer < admin < owner).
// Empty string ranks below everything.
func higherRole(a, b string) string {
	if roleLevel[a] >= roleLevel[b] {
		return a
	}
	return b
}

// composeEffectiveRole folds the three access paths on a ProjectAccess
// into a single effective project role using higherRole. Extracted
// from IsProjectAccessible so tests exercise the real production
// path instead of duplicating the folding logic.
//
// Empty return means "no access via any path" — caller should treat
// as Accessible=false (which IsProjectAccessible sets in tandem).
func composeEffectiveRole(pa *ProjectAccess) string {
	if pa == nil {
		return ""
	}
	var eff string
	if pa.ViaOwner {
		eff = "owner"
	}
	if pa.ViaMember {
		eff = higherRole(eff, pa.MemberRole)
	}
	if pa.ViaOrg {
		eff = higherRole(eff, mapOrgRoleToProjectRole(pa.OrgRole))
	}
	return eff
}

// IsProjectAccessible unions the three access paths (owner_id +
// project_members + org_members) in a single query so the caller
// doesn't have to make three round-trips or model the union in
// application code. EffectiveRole is derived from whichever path
// grants the highest privilege.
//
// **Pool constraint:** org_members is REVOKE-ALL from
// eurobase_gateway (migration 000114). This function joins
// org_members, so it MUST be called on the developer pool. Passing
// the gateway pool will fail with 42501 permission denied — same
// class of trap that bit us in the ListProjects org-union hotfix
// (see CLAUDE.md, "gateway pool CANNOT read organizations /
// org_members").
//
// Returns ErrProjectNotFound when the project id doesn't exist —
// distinguished from "exists but not accessible" (which sets
// Accessible=false without an error) so the caller can 404 vs 403.
func IsProjectAccessible(ctx context.Context, pool *pgxpool.Pool, userID, projectID string) (*ProjectAccess, error) {
	const q = `
		SELECT
		    (p.owner_id = $1::uuid)                                    AS via_owner,
		    (pm.user_id IS NOT NULL)                                   AS via_member,
		    (p.org_id IS NOT NULL AND om.platform_user_id IS NOT NULL) AS via_org,
		    COALESCE(pm.role, '')                                      AS member_role,
		    COALESCE(om.role, '')                                      AS org_role
		  FROM public.projects p
		  LEFT JOIN public.project_members pm
		         ON pm.project_id = p.id
		        AND pm.user_id = $1::uuid
		  LEFT JOIN public.org_members om
		         ON om.org_id = p.org_id
		        AND om.platform_user_id = $1::uuid
		 WHERE p.id = $2::uuid
	`
	var pa ProjectAccess
	err := pool.QueryRow(ctx, q, userID, projectID).
		Scan(&pa.ViaOwner, &pa.ViaMember, &pa.ViaOrg, &pa.MemberRole, &pa.OrgRole)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("project access lookup: %w", err)
	}
	pa.Accessible = pa.ViaOwner || pa.ViaMember || pa.ViaOrg
	pa.EffectiveRole = composeEffectiveRole(&pa)
	return &pa, nil
}
