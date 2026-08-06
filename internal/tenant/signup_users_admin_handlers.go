package tenant

// Signup-users admin dashboard — public-beta launch observability.
//
// The founders need a single place to watch signups + upgrades in
// real time once ALLOW_PUBLIC_SIGNUP=true flips. This handler
// returns every platform_users row plus derived fields (plan,
// MRR, project count) in one query.
//
// Read-only v1. Existing team-beta / legal-team-beta toggle
// endpoints stay separate; the console renders the toggles as
// per-row actions on this table that call the existing endpoints.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SignupUserEntry is one row on the /admin/signup-users dashboard.
// One row per platform_users row, joined against projects +
// subscriptions for the derived plan / MRR / count columns.
//
// Plan reflects PAID state only — 'pro' iff the user has at least
// one subscription in `status='active'`. An in-flight checkout
// (`status='incomplete'`) surfaces separately in CheckoutPending
// so the row can render a "checkout pending" indicator without
// misrepresenting the €0 case as Pro.
type SignupUserEntry struct {
	UserID              string     `json:"user_id"`
	Email               string     `json:"email"`
	DisplayName         *string    `json:"display_name"`
	SignupDate          time.Time  `json:"signup_date"`
	LastActiveAt        *time.Time `json:"last_active_at"`
	Plan                string     `json:"plan"` // "free" | "pro"
	CheckoutPending     bool       `json:"checkout_pending"`
	MRRCents            int64      `json:"mrr_cents"`
	ProjectCount        int        `json:"project_count"`
	TeamBetaAccess      bool       `json:"team_beta_access"`
	LegalTeamBetaAccess bool       `json:"legal_team_beta_access"`
}

// AdminListSignupUsers returns every platform_users row + derived
// plan / MRR / project count. Gated upstream by
// superadminMiddleware.
//
// Query design:
//   * LEFT JOIN projects — every user, even those with no projects.
//   * LEFT JOIN subscriptions — only "live" statuses count towards
//     MRR (active) and effective plan (active or incomplete —
//     incomplete means a checkout is in flight).
//   * Aggregate: any active/incomplete Pro subscription → plan="pro";
//     otherwise "free". SUM of active subscriptions' price_cents.
//   * LIMIT 500 as a defensive cap; the sweeper log-warns if we
//     hit the ceiling so we know when to add pagination.
func AdminListSignupUsers(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const q = `
			SELECT
			    u.id,
			    u.email,
			    u.display_name,
			    u.created_at,
			    u.last_sign_in_at,
			    COALESCE(u.team_beta_access, false),
			    COALESCE(u.legal_team_beta_access, false),
			    COUNT(DISTINCT p.id) AS project_count,
			    -- Plan = 'pro' only if the user has a paid subscription
			    -- (status='active'). An in-flight checkout keeps them
			    -- 'free' but flips checkout_pending so the UI can render
			    -- a pending indicator instead of a green Pro badge over
			    -- a €0 MRR.
			    COALESCE(
			        MAX(CASE WHEN s.status = 'active' THEN 'pro' END),
			        'free'
			    ) AS plan,
			    BOOL_OR(s.status = 'incomplete') AS checkout_pending,
			    COALESCE(
			        SUM(CASE WHEN s.status = 'active' THEN s.price_cents ELSE 0 END),
			        0
			    ) AS mrr_cents
			FROM public.platform_users u
			LEFT JOIN public.projects p       ON p.owner_id = u.id
			LEFT JOIN public.subscriptions s  ON s.project_id = p.id
			GROUP BY u.id
			ORDER BY u.created_at DESC
			LIMIT 500
		`
		rows, err := pool.Query(r.Context(), q)
		if err != nil {
			slog.Error("AdminListSignupUsers: query failed", "error", err)
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]SignupUserEntry, 0, 64)
		for rows.Next() {
			var e SignupUserEntry
			// BOOL_OR returns NULL when every input row's expression
			// is NULL (i.e. the user has no subscriptions at all);
			// Scan through a *bool wrapper to normalise to Go bool.
			var pending *bool
			if err := rows.Scan(
				&e.UserID, &e.Email, &e.DisplayName, &e.SignupDate, &e.LastActiveAt,
				&e.TeamBetaAccess, &e.LegalTeamBetaAccess,
				&e.ProjectCount, &e.Plan, &pending, &e.MRRCents,
			); err != nil {
				slog.Error("AdminListSignupUsers: scan failed", "error", err)
				http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
				return
			}
			if pending != nil {
				e.CheckoutPending = *pending
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			slog.Error("AdminListSignupUsers: rows.Err()", "error", err)
			http.Error(w, `{"error":"row iteration failed"}`, http.StatusInternalServerError)
			return
		}

		// Warn signal that we're approaching the LIMIT — cue to add
		// server-side pagination before we cross ~500 signups.
		if len(out) >= 500 {
			slog.Warn("AdminListSignupUsers: hit LIMIT 500 — pagination needed",
				"returned", len(out))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"users": out,
			"total": len(out),
		})
	}
}
