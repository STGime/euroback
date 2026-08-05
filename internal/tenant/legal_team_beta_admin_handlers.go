package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserHasLegalTeamBetaAccess reads the legal_team_beta_access flag off
// platform_users. Mirrors UserHasTeamBetaAccess (M2 review minor #1)
// so CreateProject can gate plan=legal_team through a single helper
// rather than an inline query — keeps the CreateProject dispatch and
// admin-lookup path from drifting on the query shape.
func UserHasLegalTeamBetaAccess(ctx context.Context, pool *pgxpool.Pool, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	var granted bool
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(legal_team_beta_access, false) FROM public.platform_users WHERE id = $1::uuid`,
		userID,
	).Scan(&granted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return granted, err
}

// Legal-Team beta admin handlers (M2b, milestone #2).
//
// Mirrors the Team-beta handler set in team_beta_admin_handlers.go
// — the only differences are:
//
//   * Reads platform_users.legal_team_beta_access instead of
//     team_beta_access.
//   * Counts p.plan = 'legal_team' active projects per user.
//   * Emits ActionLegalTeamBetaGranted / ActionLegalTeamBetaRevoked
//     audit events.
//
// Kept as a separate file so a diff of "who can grant Legal Team"
// versus "who can grant Team" stays a one-line search — the code
// is intentionally near-duplicated rather than parameterised.

// AdminListLegalTeamBetaUsers lists every platform_user with the
// Legal-Team beta flag set + the count of legal_team projects each
// has active. Gated upstream by superadminMiddleware.
func AdminListLegalTeamBetaUsers(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(),
			`SELECT u.id, u.email, u.display_name, u.legal_team_beta_granted_at,
			        grantor.email, u.created_at,
			        COALESCE(
			          (SELECT count(*) FROM public.projects p
			             WHERE p.owner_id = u.id
			               AND p.plan = 'legal_team'
			               AND p.status = 'active'),
			          0)
			   FROM public.platform_users u
			   LEFT JOIN public.platform_users grantor
			          ON grantor.id = u.legal_team_beta_granted_by
			  WHERE u.legal_team_beta_access = true
			  ORDER BY u.legal_team_beta_granted_at DESC NULLS LAST, u.created_at DESC
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

// AdminGrantLegalTeamBeta flips legal_team_beta_access = true on a
// specific platform_user, stamps granted_at/granted_by, and audits.
func AdminGrantLegalTeamBeta(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(chi.URLParam(r, "id"))
		if userID == "" {
			http.Error(w, `{"error":"user id required"}`, http.StatusBadRequest)
			return
		}
		grantorID := actorUserID(r)

		tag, err := pool.Exec(r.Context(),
			`UPDATE public.platform_users
			    SET legal_team_beta_access     = true,
			        legal_team_beta_granted_at = now(),
			        legal_team_beta_granted_by = NULLIF($2, '')::uuid
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

		writeAudit(r, audit.ActionLegalTeamBetaGranted, userID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// AdminRevokeLegalTeamBeta flips legal_team_beta_access = false.
// Prospective — existing Legal-Team projects keep running.
func AdminRevokeLegalTeamBeta(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(chi.URLParam(r, "id"))
		if userID == "" {
			http.Error(w, `{"error":"user id required"}`, http.StatusBadRequest)
			return
		}
		tag, err := pool.Exec(r.Context(),
			`UPDATE public.platform_users
			    SET legal_team_beta_access = false
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

		writeAudit(r, audit.ActionLegalTeamBetaRevoked, userID)
		w.WriteHeader(http.StatusNoContent)
	}
}
