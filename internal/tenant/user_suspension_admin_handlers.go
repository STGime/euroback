package tenant

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminSuspendUser suspends a platform user (superadmin only): they can no
// longer sign in (any login path refuses to mint a session) or create a
// project. Existing JWTs stay valid until they expire (<= 24 h). Does not
// touch the user's projects, tokens or data.
func AdminSuspendUser(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(chi.URLParam(r, "id"))
		if userID == "" {
			http.Error(w, `{"error":"user id required"}`, http.StatusBadRequest)
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r := []rune(body.Reason); len(r) > 500 { // rune-safe: the CHECK is length() (chars)
			body.Reason = string(r[:500])
		}
		actor := actorUserID(r)

		// A superadmin can't suspend themselves (would lock the platform's
		// own admin out), and can't suspend another superadmin.
		var targetSuper bool
		if err := pool.QueryRow(r.Context(),
			`SELECT COALESCE(is_superadmin, false) FROM public.platform_users WHERE id = $1::uuid`, userID).Scan(&targetSuper); err != nil {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}
		if userID == actor {
			http.Error(w, `{"error":"you can't suspend your own account","code":"self_suspend"}`, http.StatusBadRequest)
			return
		}
		if targetSuper {
			http.Error(w, `{"error":"can't suspend a superadmin","code":"suspend_superadmin"}`, http.StatusForbidden)
			return
		}

		tag, err := pool.Exec(r.Context(),
			`UPDATE public.platform_users
			    SET suspended_at     = COALESCE(suspended_at, now()),
			        suspended_reason = NULLIF($2, ''),
			        suspended_by     = NULLIF($3, '')::uuid
			  WHERE id = $1::uuid`,
			userID, body.Reason, actor)
		if err != nil {
			http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}
		writeAudit(r, audit.ActionUserSuspended, userID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// AdminUnsuspendUser clears a suspension (superadmin only).
func AdminUnsuspendUser(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(chi.URLParam(r, "id"))
		if userID == "" {
			http.Error(w, `{"error":"user id required"}`, http.StatusBadRequest)
			return
		}
		tag, err := pool.Exec(r.Context(),
			`UPDATE public.platform_users
			    SET suspended_at = NULL, suspended_reason = NULL, suspended_by = NULL
			  WHERE id = $1::uuid`, userID)
		if err != nil {
			http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
			return
		}
		writeAudit(r, audit.ActionUserUnsuspended, userID)
		w.WriteHeader(http.StatusNoContent)
	}
}
