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
// Safety guards:
//   - Refuses to delete the calling superadmin themselves. Self-delete
//     is available via /platform/auth/account/delete after the caller
//     has manually deleted their projects.
//   - Refuses to delete another superadmin. Ops trying to demote /
//     remove a peer must revoke is_superadmin first via SQL — a
//     superadmin's project set is likely load-bearing.
//
// URL param `id` is the target user's UUID.
func AdminDeleteUser(pool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
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

		// Refuse if the target is the sole admin of any organization.
		// Deleting them cascades org_members away and leaves the org
		// (and any org-owned projects with a different owner_id)
		// unmanageable — no one can add members, edit SSO, or
		// transfer projects out. Ops must hand off admin rights to
		// another member first. Matches the same guard the ops
		// runbook (docs/runbooks/grant-team-beta-access.md) documents
		// for the team_beta_access revoke path.
		var orphanedOrgs int
		err = pool.QueryRow(r.Context(),
			`SELECT count(*) FROM public.org_members m
			  WHERE m.user_id = $1::uuid
			    AND m.role = 'admin'
			    AND NOT EXISTS (
			      SELECT 1 FROM public.org_members m2
			       WHERE m2.org_id = m.org_id
			         AND m2.role = 'admin'
			         AND m2.user_id <> m.user_id
			    )`,
			targetUserID,
		).Scan(&orphanedOrgs)
		if err != nil {
			slog.Error("admin delete user: sole-admin check failed",
				"target_user_id", targetUserID, "error", err)
			http.Error(w, `{"error":"sole-admin check failed"}`, http.StatusInternalServerError)
			return
		}
		if orphanedOrgs > 0 {
			http.Error(w, `{"error":"refusing to delete — user is sole admin of one or more organizations; hand off admin to another member first"}`, http.StatusConflict)
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

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deleted":          true,
			"email":            email,
			"projects_deleted": len(projectIDs),
		})
	}
}
