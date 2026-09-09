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

// ProjectAccess describes how a user reaches a project.
type ProjectAccess struct {
	Accessible bool
	// ViaOwner is true when the user matches projects.owner_id
	// (the pre-org path — every existing project resolves this way).
	ViaOwner bool
	// ViaOrg is true when the user matches an org_members row for
	// the project's org_id. Mutually non-exclusive with ViaOwner —
	// an owner who is also an org admin resolves both.
	ViaOrg bool
	// OrgRole is the caller's role in the project's org, when
	// applicable. Empty when the project is per-user (org_id NULL)
	// or when access is via owner_id only.
	OrgRole string
}

// IsProjectAccessible unions the two access paths (owner_id +
// org_members) in a single query so the caller doesn't have to make
// two round-trips or model the "either" logic in application code.
//
// Runs against the pool the caller provides — typically the
// developer pool from the /platform admin surface, or the gateway
// pool when serving /platform/projects endpoints. Both pools have
// SELECT on projects, and (per migration 000114) both pools have
// SELECT on org_members via the developer path; the query works on
// either.
//
// Returns ErrProjectNotFound when the project id doesn't exist —
// distinguished from "exists but not accessible" (which sets
// Accessible=false without an error) so the caller can 404 vs 403.
func IsProjectAccessible(ctx context.Context, pool *pgxpool.Pool, userID, projectID string) (*ProjectAccess, error) {
	const q = `
		SELECT
		    (p.owner_id = $1::uuid)                      AS via_owner,
		    (p.org_id IS NOT NULL AND om.platform_user_id IS NOT NULL) AS via_org,
		    COALESCE(om.role, '')                        AS org_role
		  FROM public.projects p
		  LEFT JOIN public.org_members om
		         ON om.org_id = p.org_id
		        AND om.platform_user_id = $1::uuid
		 WHERE p.id = $2::uuid
	`
	var pa ProjectAccess
	err := pool.QueryRow(ctx, q, userID, projectID).Scan(&pa.ViaOwner, &pa.ViaOrg, &pa.OrgRole)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("project access lookup: %w", err)
	}
	pa.Accessible = pa.ViaOwner || pa.ViaOrg
	return &pa, nil
}
