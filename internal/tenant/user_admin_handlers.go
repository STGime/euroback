package tenant

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminDeleteUser deletes a platform user + every project they own,
// including the Scaleway teardown for any Team-tier dedicated instance
// on those projects. Superadmin-only (gated upstream by
// superadminMiddleware).
//
// Why per-project DeleteProject before the platform_users row: the FK
// on public.project_databases → projects is ON DELETE RESTRICT
// (migration 000083), so a raw DELETE FROM platform_users would
// cascade to projects and fail with 23503 for any Team-tier user. Even
// if it didn't, the cascade would leave the Scaleway managed-PG
// instance orphaned — TenantService.DeleteProject does the
// provider-side teardown inline before the row is dropped. Reuse it
// rather than re-implement.
//
// Two pools by design:
//   - `pool` is the gateway pool. Used for the platform_users lookup,
//     the projects read, and the final platform_users DELETE. The
//     platform_users→org_members / subscriptions / etc. CASCADEs
//     fire on this pool without any grant issue (RI cascades don't
//     check the caller's DML privilege on the child).
//   - `developerPool` is the developer pool. Used for the sole-admin
//     org check ONLY, because migration 000114 REVOKEs ALL on
//     public.org_members from eurobase_gateway. Running the check on
//     `pool` would 42501. Same rule the console's own project-list
//     path enforces (see CLAUDE.md, "gateway pool CANNOT read
//     organizations / org_members").
//
// Safety guards:
//   - Refuses to delete the calling superadmin themselves. Self-delete
//     is available via /platform/auth/account/delete after the caller
//     has manually deleted their projects.
//   - Refuses to delete another superadmin. Ops trying to demote /
//     remove a peer must revoke is_superadmin first via SQL — a
//     superadmin's project set is likely load-bearing.
//   - Refuses to delete a user who is the sole admin of an org that
//     ALSO has other (non-admin) members. Those other members would
//     be left with no admin, unable to invite anyone, edit SSO, or
//     transfer projects out. Ops must hand off admin first.
//   - When the target is the sole member of an org entirely (no other
//     admins, no other members), the org is deleted alongside the
//     user. Otherwise every user with a personal org would need
//     admin-side hand-off help to delete their account. Cascades to
//     org_members (empty by construction); projects.org_id →
//     ON DELETE SET NULL, so any attached project becomes a
//     personal project again.
//
// URL param `id` is the target user's UUID.
func AdminDeleteUser(pool, developerPool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targetUserID := strings.TrimSpace(chi.URLParam(r, "id"))
		if targetUserID == "" {
			http.Error(w, `{"error":"user id required"}`, http.StatusBadRequest)
			return
		}
		actorID := actorUserID(r)
		if actorID != "" && actorID == targetUserID {
			http.Error(w, `{"error":"cannot delete your own account via admin — use /platform/auth/account/delete"}`, http.StatusBadRequest)
			return
		}

		// Look up target row + refuse if superadmin.
		var email string
		var isSuperadmin bool
		err := pool.QueryRow(r.Context(),
			`SELECT email, COALESCE(is_superadmin, false)
			   FROM public.platform_users
			  WHERE id = $1::uuid`,
			targetUserID,
		).Scan(&email, &isSuperadmin)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"lookup failed"}`, http.StatusInternalServerError)
			return
		}
		if isSuperadmin {
			http.Error(w, `{"error":"refusing to delete a superadmin — revoke is_superadmin first"}`, http.StatusBadRequest)
			return
		}

		// Sort orgs the target belongs to into three buckets, all in
		// one round-trip. Reads org_members on the DEVELOPER pool:
		// migration 000114 REVOKEs ALL on public.org_members from
		// eurobase_gateway. Same rule ListProjects had to be split on
		// (see the #538/#545/#546 lesson in CLAUDE.md) — reads of org
		// tables live on the developer pool, DML on platform_users /
		// projects stays on the gateway pool.
		//
		//   category = 'survives'   → another admin exists; org lives
		//                             on after cascade removes the
		//                             target's membership.
		//   category = 'orphaned'   → target is sole admin AND there
		//                             are other (non-admin) members.
		//                             REFUSE — hand-off required.
		//   category = 'solo'       → target is the only member of any
		//                             role. Delete the org alongside
		//                             the user; nothing to orphan.
		//
		// The classification is per-org because a single target can
		// simultaneously be a survives-org member and a solo-org owner.
		orgRows, err := developerPool.Query(r.Context(),
			`SELECT m.org_id::text,
			        CASE
			          WHEN EXISTS (
			                 SELECT 1 FROM public.org_members m2
			                  WHERE m2.org_id = m.org_id
			                    AND m2.role = 'admin'
			                    AND m2.platform_user_id <> m.platform_user_id
			               ) THEN 'survives'
			          WHEN EXISTS (
			                 SELECT 1 FROM public.org_members m2
			                  WHERE m2.org_id = m.org_id
			                    AND m2.platform_user_id <> m.platform_user_id
			               ) THEN 'orphaned'
			          ELSE 'solo'
			        END AS category
			   FROM public.org_members m
			  WHERE m.platform_user_id = $1::uuid`,
			targetUserID,
		)
		if err != nil {
			slog.Error("admin delete user: org classification failed",
				"target_user_id", targetUserID, "error", err)
			http.Error(w, `{"error":"org membership check failed"}`, http.StatusInternalServerError)
			return
		}
		var soloOrgIDs []string
		var orphanedCount int
		for orgRows.Next() {
			var orgID, category string
			if err := orgRows.Scan(&orgID, &category); err != nil {
				orgRows.Close()
				slog.Error("admin delete user: scan org classification failed",
					"target_user_id", targetUserID, "error", err)
				http.Error(w, `{"error":"org membership scan failed"}`, http.StatusInternalServerError)
				return
			}
			switch category {
			case "solo":
				soloOrgIDs = append(soloOrgIDs, orgID)
			case "orphaned":
				orphanedCount++
			}
		}
		// rows.Err() surfaces mid-iteration failures (conn drop,
		// cancelled ctx). Without it we'd fail OPEN: an `orphaned`
		// org past the cut-off would go uncounted and the guard
		// would let the delete through, stranding the exact members
		// it exists to protect.
		if rowsErr := orgRows.Err(); rowsErr != nil {
			orgRows.Close()
			slog.Error("admin delete user: org classification iteration failed",
				"target_user_id", targetUserID, "error", rowsErr)
			http.Error(w, `{"error":"org membership iteration failed"}`, http.StatusInternalServerError)
			return
		}
		orgRows.Close()
		if orphanedCount > 0 {
			http.Error(w, `{"error":"refusing to delete — user is sole admin of one or more organizations that still have other members; hand off admin to another member first"}`, http.StatusConflict)
			return
		}

		// Enumerate the target's projects. owner_id is FK'd with
		// ON DELETE CASCADE to platform_users, but we need to
		// call DeleteProject for the Scaleway teardown side-effect
		// on any Team-tier row, so we iterate explicitly.
		rows, err := pool.Query(r.Context(),
			`SELECT id FROM public.projects WHERE owner_id = $1::uuid`,
			targetUserID,
		)
		if err != nil {
			http.Error(w, `{"error":"list projects failed"}`, http.StatusInternalServerError)
			return
		}
		var projectIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				http.Error(w, `{"error":"scan project id failed"}`, http.StatusInternalServerError)
				return
			}
			projectIDs = append(projectIDs, id)
		}
		rows.Close()

		// Delete each project. Any failure aborts the whole
		// admin-delete — better to leave the user record + some
		// projects intact than to hard-delete platform_users while
		// leaving orphaned projects/instances behind. Ops can retry
		// after fixing the failure (e.g. Scaleway 5xx during
		// teardown).
		for _, projectID := range projectIDs {
			if err := svc.DeleteProject(r.Context(), projectID); err != nil {
				slog.Error("admin delete user: DeleteProject failed — aborting user delete",
					"target_user_id", targetUserID,
					"project_id", projectID,
					"error", err)
				http.Error(w, `{"error":"delete project failed — user not deleted, retry after resolving the error"}`, http.StatusInternalServerError)
				return
			}
		}

		// Guard the enumerate-then-delete race: if the target
		// created a fresh project between the SELECT above and
		// this point, the platform_users cascade would tear the
		// row out (owner_id FK is ON DELETE CASCADE) but skip the
		// Scaleway teardown — the exact leak this endpoint was
		// designed to prevent. Refuse rather than silently orphan.
		// Extremely narrow race (target creating a project during
		// their own deletion), but cheap to guard.
		var remaining int
		if err := pool.QueryRow(r.Context(),
			`SELECT count(*) FROM public.projects WHERE owner_id = $1::uuid`,
			targetUserID,
		).Scan(&remaining); err != nil {
			slog.Error("admin delete user: post-loop project re-check failed",
				"target_user_id", targetUserID, "error", err)
			http.Error(w, `{"error":"post-loop project check failed"}`, http.StatusInternalServerError)
			return
		}
		if remaining > 0 {
			slog.Warn("admin delete user: project appeared during teardown — aborting to avoid Scaleway leak",
				"target_user_id", targetUserID,
				"remaining_projects", remaining)
			http.Error(w, `{"error":"a new project appeared during teardown — retry the delete"}`, http.StatusConflict)
			return
		}

		// Finally delete the user. Cascades to org_members,
		// subscriptions, platform_email_tokens, legal_acceptances,
		// personal_access_tokens, etc. via their platform_users FK
		// ON DELETE CASCADE clauses.
		tag, err := pool.Exec(r.Context(),
			`DELETE FROM public.platform_users WHERE id = $1::uuid`,
			targetUserID,
		)
		if err != nil {
			slog.Error("admin delete user: platform_users delete failed",
				"target_user_id", targetUserID,
				"projects_already_deleted", len(projectIDs),
				"error", err)
			http.Error(w, `{"error":"delete user failed"}`, http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			// Race: someone else deleted the row between the lookup
			// and here. Idempotent from the caller's perspective —
			// but skip the audit write so we don't record a
			// user.deleted event for a no-op.
			slog.Info("admin delete user: row already gone",
				"target_user_id", targetUserID)
		} else {
			slog.Info("admin delete user completed",
				"target_user_id", targetUserID,
				"email", email,
				"projects_deleted", len(projectIDs),
				"actor_id", actorID)
			writeAudit(r, audit.ActionUserDeleted, targetUserID)
		}

		// Clean up solo orgs — the target was the only member, so the
		// FK cascade left the org row with created_by=NULL and zero
		// members: unmanageable and useless. Runs after the user
		// delete so the cascade has already emptied org_members.
		//
		// The extra `NOT EXISTS` in the DELETE guards a narrow race:
		// someone accepts an invite between our classification query
		// and here. If it fires, the org survives untouched — we skip
		// this DELETE and leave the cleanup to whoever now owns it.
		orgsDeleted := 0
		for _, orgID := range soloOrgIDs {
			del, err := developerPool.Exec(r.Context(),
				`DELETE FROM public.organizations o
				  WHERE o.id = $1::uuid
				    AND NOT EXISTS (
				      SELECT 1 FROM public.org_members m
				       WHERE m.org_id = o.id
				    )`,
				orgID,
			)
			if err != nil {
				// Non-fatal — the user is already gone. Log and move on;
				// ops can sweep zombie orgs later. Alternative is 500
				// with the user already deleted, which is worse.
				slog.Warn("admin delete user: solo-org cleanup failed",
					"org_id", orgID,
					"target_user_id", targetUserID,
					"error", err)
				continue
			}
			if del.RowsAffected() > 0 {
				orgsDeleted++
			}
		}
		if orgsDeleted > 0 {
			slog.Info("admin delete user: solo orgs cleaned up",
				"target_user_id", targetUserID,
				"orgs_deleted", orgsDeleted)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deleted":          true,
			"email":            email,
			"projects_deleted": len(projectIDs),
			"orgs_deleted":     orgsDeleted,
		})
	}
}
