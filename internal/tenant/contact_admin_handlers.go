package tenant

// Admin triage endpoints for the marketing-site contact form
// (public visitor submissions written by /platform/public/contact,
// stored in public.contact_requests per migration 000112).
//
// Two routes, both under superadminMiddleware:
//   GET  /platform/admin/contact-requests?state=unresolved|all
//   POST /platform/admin/contact-requests/{id}/resolve  (body: {"note": "..."})
//
// These run against the eurobase_developer pool (migration 000112
// grants SELECT + UPDATE), so no separate role-switching is needed.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContactRequestEntry is one row of /admin/contact-requests.
type ContactRequestEntry struct {
	ID             string     `json:"id"`
	Name           *string    `json:"name"`
	Email          string     `json:"email"`
	Message        string     `json:"message"`
	Source         string     `json:"source"`
	UserAgent      *string    `json:"user_agent"`
	IPAddress      *string    `json:"ip_address"`
	CreatedAt      time.Time  `json:"created_at"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	ResolvedByID   *string    `json:"resolved_by_id"`
	ResolvedBy     *string    `json:"resolved_by"` // email of the resolver
	ResolutionNote *string    `json:"resolution_note"`
}

// contactResolveMaxNoteLen bounds the free-text resolution note.
// Matches the same "short internal annotation, not a novel" shape
// as the backup-tag validator; overlong notes get a clean 400
// rather than a driver-side error.
const contactResolveMaxNoteLen = 500

// AdminListContactRequests returns marketing-site contact form
// submissions for the admin triage view. Newest-first, unresolved
// by default (query `?state=all` to include already-resolved rows).
//
// Gated upstream by superadminMiddleware. Runs on the developer
// pool with SELECT rights on public.contact_requests granted by
// migration 000112.
func AdminListContactRequests(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		showAll := r.URL.Query().Get("state") == "all"

		// Two variants of the query so the WHERE resolved_at IS NULL
		// path can use the partial index from migration 000112.
		//
		// Design notes from the /admin "Row iteration failed" bug:
		//   * `id` and `resolved_by` are UUID columns; scan into string
		//     via ::text cast so pgx v5 doesn't have to negotiate the
		//     uuid-to-string codec (the *string form under LEFT JOIN
		//     was surfacing a lazy driver error as row-iteration-failed
		//     in prod).
		//   * `ip_address` is INET; ::text cast for the same reason
		//     (host() would work too but ::text is more mechanical).
		//   * Dropped the LEFT JOIN to platform_users for the resolver
		//     email. The frontend already loads AdminListSignupUsers on
		//     the same admin page, so it can render the resolver's
		//     email from that list by UUID lookup — one less join
		//     surface here, one less driver quirk to hit.
		var q string
		if showAll {
			q = `
				SELECT c.id::text, c.name, c.email, c.message, c.source,
				       c.user_agent, c.ip_address::text,
				       c.created_at, c.resolved_at, c.resolved_by::text,
				       c.resolution_note
				  FROM public.contact_requests c
				 ORDER BY c.created_at DESC
				 LIMIT 500
			`
		} else {
			q = `
				SELECT c.id::text, c.name, c.email, c.message, c.source,
				       c.user_agent, c.ip_address::text,
				       c.created_at, c.resolved_at, c.resolved_by::text,
				       c.resolution_note
				  FROM public.contact_requests c
				 WHERE c.resolved_at IS NULL
				 ORDER BY c.created_at DESC
				 LIMIT 500
			`
		}

		rows, err := pool.Query(r.Context(), q)
		if err != nil {
			// Include err.Error() in the response body so the
			// admin-page error banner surfaces the real cause on the
			// next regression (was suppressed to a generic "query
			// failed" and cost us a round-trip to prod logs).
			slog.Error("AdminListContactRequests: query failed", "error", err)
			http.Error(w, `{"error":"query failed: `+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]ContactRequestEntry, 0, 32)
		for rows.Next() {
			var e ContactRequestEntry
			if err := rows.Scan(
				&e.ID, &e.Name, &e.Email, &e.Message, &e.Source,
				&e.UserAgent, &e.IPAddress,
				&e.CreatedAt, &e.ResolvedAt, &e.ResolvedByID,
				&e.ResolutionNote,
			); err != nil {
				slog.Error("AdminListContactRequests: scan failed", "error", err)
				http.Error(w, `{"error":"scan failed: `+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			slog.Error("AdminListContactRequests: rows.Err()", "error", err)
			http.Error(w, `{"error":"row iteration failed: `+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		if len(out) >= 500 {
			slog.Warn("AdminListContactRequests: hit LIMIT 500 — pagination needed", "returned", len(out))
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"requests": out,
			"total":    len(out),
			"state":    ternary(showAll, "all", "unresolved"),
		})
	}
}

// AdminResolveContactRequest marks a contact request as handled.
// Body: {"note": "optional short annotation"}. The `resolved_by`
// column takes the superadmin's platform_users.id from the request
// context (superadminMiddleware attaches it).
//
// Idempotent: a second resolve on the same row updates the note and
// bumps resolved_at to now(). We could preserve the first resolution
// timestamp instead, but a superadmin re-marking a row usually means
// "I just handled this again", so the fresh timestamp is more useful
// than the first-touch one.
func AdminResolveContactRequest(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, `{"error":"id is required"}`, http.StatusBadRequest)
			return
		}

		var body struct {
			Note string `json:"note"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
				return
			}
		}
		body.Note = strings.TrimSpace(body.Note)
		if utf8.RuneCountInString(body.Note) > contactResolveMaxNoteLen {
			http.Error(w, `{"error":"note must be 500 characters or fewer"}`, http.StatusBadRequest)
			return
		}

		// resolvedBy comes from the superadmin's JWT claims —
		// superadminMiddleware guarantees an is-superadmin caller
		// past this point, but re-check the claims presence so a
		// wiring bug (route mounted outside the middleware group)
		// surfaces loudly rather than crashing on a nil UUID cast.
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil || claims.Subject == "" {
			slog.Error("AdminResolveContactRequest: missing claims in ctx (route wired outside superadminMiddleware?)")
			http.Error(w, `{"error":"session missing"}`, http.StatusUnauthorized)
			return
		}
		resolvedBy := claims.Subject

		var notePtr *string
		if body.Note != "" {
			notePtr = &body.Note
		}

		var resolvedAt time.Time
		err := pool.QueryRow(r.Context(), `
			UPDATE public.contact_requests
			   SET resolved_at     = now(),
			       resolved_by     = $2::uuid,
			       resolution_note = $3
			 WHERE id = $1::uuid
			 RETURNING resolved_at
		`, id, resolvedBy, notePtr).Scan(&resolvedAt)
		if err == pgx.ErrNoRows {
			http.Error(w, `{"error":"contact request not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			slog.Error("AdminResolveContactRequest: update failed", "error", err, "id", id)
			http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          id,
			"resolved_at": resolvedAt,
		})
	}
}

// ternary is a tiny helper to keep the state string readable inline.
func ternary[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}
