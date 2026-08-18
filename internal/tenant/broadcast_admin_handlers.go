package tenant

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BroadcastRecipient is one row on GET /platform/admin/broadcast/audience.
// The endpoint returns the deduped union of platform_allowlist.email and
// platform_users.email; each entry annotates its provenance so the
// console can render "N invited, M signed up, K in both" without
// re-fetching either source list.
//
// Email is the canonical lowercased form — matches what the send
// handler compares against in its ANY($1) validation. The console
// stores + sends this form so the round-trip is byte-consistent.
type BroadcastRecipient struct {
	Email       string     `json:"email"`
	OnAllowlist bool       `json:"on_allowlist"`
	HasAccount  bool       `json:"has_account"`
	SignedUpAt  *time.Time `json:"signed_up_at"`
}

// AdminListBroadcastAudience returns the deduped union of the two
// recipient sources — closed-beta invitations and actual signups.
//
// Design:
//
//   - `lower(trim(email))` on both sides so `Foo@X.com` and
//     `foo@x.com` collapse to one recipient. platform_users.email is
//     already lowercased at signup, but platform_allowlist.email is
//     free-typed by superadmin so we normalize here as belt.
//   - FULL OUTER JOIN so an email in one table but not the other
//     still appears once. Coalesced email is the lowercased key.
//   - platform_users.created_at → signed_up_at when has_account=true.
//     NULL when the recipient is invited but never signed up.
//   - Sort by signed_up_at DESC NULLS LAST so the recently-active
//     users appear first (matches the vibe of the signup-users
//     dashboard). Purely presentational.
//
// Same superadminMiddleware gate as the other admin routes.
// LIMIT 5000 as a defensive cap — sends are BCC'd in chunks (see
// email.SendBulkBCC), but 5000 is well past the current audience
// size and a runaway broadcast to more than that should be a
// deliberate action requiring a code change.
func AdminListBroadcastAudience(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Within-source dedup MUST happen inside each subquery
		// BEFORE the join. platform_allowlist.email is a
		// case-sensitive TEXT PRIMARY KEY (migration 000034), so a
		// superadmin can insert `Foo@X.com` AND `foo@x.com` as two
		// distinct rows — a naive FULL OUTER JOIN would produce two
		// output rows for the same normalized address, breaking the
		// "each exactly once" guarantee at the audience level and
		// at the send level. `SELECT DISTINCT lower(trim(email))`
		// on the allowlist side collapses those. Users side uses
		// GROUP BY (rather than DISTINCT) because we also need a
		// deterministic created_at from potentially multiple rows —
		// MAX(created_at) picks the latest so the audience is sorted
		// by the user's most-recent presence.
		const q = `
			SELECT
			    COALESCE(u_email, a_email)     AS email,
			    a_email IS NOT NULL            AS on_allowlist,
			    u_email IS NOT NULL            AS has_account,
			    u_signed_up_at                 AS signed_up_at
			FROM (
			    SELECT DISTINCT lower(trim(email)) AS a_email
			      FROM public.platform_allowlist
			) a
			FULL OUTER JOIN (
			    SELECT lower(trim(email)) AS u_email, max(created_at) AS u_signed_up_at
			      FROM public.platform_users
			     GROUP BY lower(trim(email))
			) u ON a.a_email = u.u_email
			WHERE COALESCE(u_email, a_email) IS NOT NULL
			ORDER BY u_signed_up_at DESC NULLS LAST, COALESCE(u_email, a_email)
			LIMIT 5000
		`
		rows, err := pool.Query(r.Context(), q)
		if err != nil {
			slog.Error("AdminListBroadcastAudience: query failed", "error", err)
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]BroadcastRecipient, 0, 128)
		var (
			totalAllowlistOnly, totalUsersOnly, totalBoth int
		)
		for rows.Next() {
			var e BroadcastRecipient
			if err := rows.Scan(&e.Email, &e.OnAllowlist, &e.HasAccount, &e.SignedUpAt); err != nil {
				slog.Error("AdminListBroadcastAudience: scan failed", "error", err)
				http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
				return
			}
			out = append(out, e)
			switch {
			case e.OnAllowlist && e.HasAccount:
				totalBoth++
			case e.OnAllowlist:
				totalAllowlistOnly++
			case e.HasAccount:
				totalUsersOnly++
			}
		}
		if err := rows.Err(); err != nil {
			slog.Error("AdminListBroadcastAudience: iterate failed", "error", err)
			http.Error(w, `{"error":"iterate failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"recipients":            out,
			"total":                 len(out),
			"total_allowlist_only":  totalAllowlistOnly,
			"total_users_only":      totalUsersOnly,
			"total_both":            totalBoth,
		})
	}
}
