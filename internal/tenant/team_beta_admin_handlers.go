package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TeamBetaEntry is the shape returned to the admin panel — one row
// per platform_user with the beta flag set, plus the count of Team
// projects they've already created. Used by the closed-beta admin UI
// (issue #308) to show operators who's granted and how many Team
// projects they've spun up.
type TeamBetaEntry struct {
	UserID            string     `json:"user_id"`
	Email             string     `json:"email"`
	DisplayName       *string    `json:"display_name,omitempty"`
	GrantedAt         *time.Time `json:"granted_at,omitempty"`
	GrantedByEmail    *string    `json:"granted_by_email,omitempty"`
	ActiveTeamProjects int        `json:"active_team_projects"`
	CreatedAt         time.Time  `json:"created_at"`
}

// AdminListTeamBetaUsers lists every platform_user with team_beta_access
// = true, plus the count of Team-tier projects each has active. Gated
// upstream by superadminMiddleware.
func AdminListTeamBetaUsers(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(),
			`SELECT u.id, u.email, u.display_name, u.team_beta_granted_at,
			        grantor.email, u.created_at,
			        COALESCE(
			          (SELECT count(*) FROM public.projects p
			             WHERE p.owner_id = u.id
			               AND p.plan = 'team'
			               AND p.status = 'active'),
			          0)
			   FROM public.platform_users u
			   LEFT JOIN public.platform_users grantor
			          ON grantor.id = u.team_beta_granted_by
			  WHERE u.team_beta_access = true
			  ORDER BY u.team_beta_granted_at DESC NULLS LAST, u.created_at DESC
			  LIMIT 500`,
		)
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]TeamBetaEntry, 0)
		for rows.Next() {
			var e TeamBetaEntry
			if err := rows.Scan(&e.UserID, &e.Email, &e.DisplayName, &e.GrantedAt,
				&e.GrantedByEmail, &e.CreatedAt, &e.ActiveTeamProjects); err != nil {
				http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
				return
			}
			out = append(out, e)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entries": out,
			"total":   len(out),
		})
	}
}

// AdminGrantTeamBeta flips team_beta_access = true on a specific
// platform_user, stamps granted_at/granted_by, and audits.
// URL param `id` is the target user's UUID.
func AdminGrantTeamBeta(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(chi.URLParam(r, "id"))
		if userID == "" {
			http.Error(w, `{"error":"user id required"}`, http.StatusBadRequest)
			return
		}
		grantorID := actorUserID(r)

		tag, err := pool.Exec(r.Context(),
			`UPDATE public.platform_users
			    SET team_beta_access     = true,
			        team_beta_granted_at = now(),
			        team_beta_granted_by = NULLIF($2, '')::uuid
			  WHERE id = $1::uuid`,
			userID, grantorID)
		if err != nil {
			http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}

		writeAudit(r, audit.ActionTeamBetaGranted, userID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// AdminRevokeTeamBeta flips team_beta_access = false. Revocation is
// prospective: it does not tear down existing Team projects (a beta
// user who then loses beta access keeps whatever they already
// created). If ops wants to reclaim the underlying provider
// instances, they must delete the projects manually — the
// deprovision worker sweeps the dedicated DBs 7 days later.
func AdminRevokeTeamBeta(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(chi.URLParam(r, "id"))
		if userID == "" {
			http.Error(w, `{"error":"user id required"}`, http.StatusBadRequest)
			return
		}
		tag, err := pool.Exec(r.Context(),
			`UPDATE public.platform_users
			    SET team_beta_access = false
			  WHERE id = $1::uuid`,
			userID)
		if err != nil {
			http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}

		writeAudit(r, audit.ActionTeamBetaRevoked, userID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// UserHasTeamBetaAccess reads the flag off platform_users. Called
// from the profile handler + CreateProject dispatch. Missing row is
// treated as false (not found).
//
// Takes context.Context (not *http.Request) so CreateProject can call
// it without holding the request — the two callers stay in sync.
func UserHasTeamBetaAccess(ctx context.Context, pool *pgxpool.Pool, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	var granted bool
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(team_beta_access, false) FROM public.platform_users WHERE id = $1::uuid`,
		userID,
	).Scan(&granted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return granted, err
}

// actorUserID pulls the current platform user's UUID out of the
// request's auth claims. Returns "" if unauthenticated; callers
// should already be behind superadmin middleware but the helper is
// defensive.
func actorUserID(r *http.Request) string {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || claims == nil {
		return ""
	}
	return claims.Subject
}

// writeAudit is a small local helper — the pattern is repeated
// across every admin handler and inlining it would triple the
// bookkeeping code.
func writeAudit(r *http.Request, action, targetUserID string) {
	svc := audit.FromContext(r.Context())
	if svc == nil {
		return
	}
	actorID, actorEmail := audit.ActorFromContext(r.Context())
	svc.Log(r.Context(), "", actorID, actorEmail, action,
		audit.WithTarget("platform_user", targetUserID),
		audit.WithIP(r.RemoteAddr))
}
