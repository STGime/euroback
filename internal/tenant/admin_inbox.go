package tenant

// Admin inbox: the messages people send us through the console — Team-tier
// support tickets (public.support_requests) and contact / Team-beta requests
// (public.contact_requests) — listed in full for superadmins, who mark them
// resolved (with a note) or reopen them. The Discord notices only carry a
// preview; this is where a message is read and handled.
//
// Both tables are revoked from eurobase_gateway (000112, 000115), so these
// handlers run on the developer pool.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InboxItem is one message in the admin inbox.
type InboxItem struct {
	Kind            string     `json:"kind"` // "support" or "contact"
	ID              string     `json:"id"`
	CreatedAt       time.Time  `json:"created_at"`
	Email           string     `json:"email"`
	Name            string     `json:"name,omitempty"`
	Subject         string     `json:"subject,omitempty"`
	Message         string     `json:"message"`
	Category        string     `json:"category,omitempty"`
	Priority        string     `json:"priority,omitempty"`
	Source          string     `json:"source,omitempty"`
	ProjectID       *string    `json:"project_id,omitempty"`
	ProjectSlug     *string    `json:"project_slug,omitempty"`
	OrgID           *string    `json:"org_id,omitempty"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	ResolvedByEmail *string    `json:"resolved_by_email,omitempty"`
	ResolutionNote  *string    `json:"resolution_note,omitempty"`
}

const inboxMaxLimit = 500

// AdminListInbox serves GET /platform/admin/inbox?status=open|resolved|all&limit=N
// (default open, 200): both kinds, newest first, plus the number of open
// messages.
func AdminListInbox(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		if status == "" {
			status = "open"
		}
		if status != "open" && status != "resolved" && status != "all" {
			writeJSONError(w, http.StatusBadRequest, "status must be open, resolved or all")
			return
		}
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				writeJSONError(w, http.StatusBadRequest, "limit must be a positive number")
				return
			}
			limit = min(n, inboxMaxLimit)
		}

		rows, err := pool.Query(r.Context(), `
			SELECT kind, id, created_at, email, name, subject, message, category, priority, source,
			       project_id, project_slug, org_id, resolved_at, resolved_by_email, resolution_note
			  FROM (
			    SELECT 'support' AS kind, s.id::text, s.created_at, s.email, NULL::text AS name, s.subject,
			           s.message, s.category, s.priority, NULL::text AS source,
			           s.project_id::text, p.slug AS project_slug, s.org_id::text,
			           s.resolved_at, ru.email AS resolved_by_email, s.resolution_note
			      FROM public.support_requests s
			      LEFT JOIN public.projects p        ON p.id = s.project_id
			      LEFT JOIN public.platform_users ru ON ru.id = s.resolved_by
			    UNION ALL
			    SELECT 'contact', c.id::text, c.created_at, c.email, c.name, NULL,
			           c.message, NULL, NULL, c.source,
			           NULL, NULL, NULL,
			           c.resolved_at, ru.email, c.resolution_note
			      FROM public.contact_requests c
			      LEFT JOIN public.platform_users ru ON ru.id = c.resolved_by
			  ) m
			 WHERE $1 = 'all' OR ($1 = 'open') = (resolved_at IS NULL)
			 ORDER BY created_at DESC
			 LIMIT $2`, status, limit)
		if err != nil {
			slog.Error("admin inbox: list failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to load the inbox")
			return
		}
		items := make([]InboxItem, 0)
		for rows.Next() {
			var it InboxItem
			var name, subject, category, priority, source *string
			if err := rows.Scan(&it.Kind, &it.ID, &it.CreatedAt, &it.Email, &name, &subject, &it.Message,
				&category, &priority, &source, &it.ProjectID, &it.ProjectSlug, &it.OrgID,
				&it.ResolvedAt, &it.ResolvedByEmail, &it.ResolutionNote); err != nil {
				rows.Close()
				slog.Error("admin inbox: scan failed", "error", err)
				writeJSONError(w, http.StatusInternalServerError, "failed to load the inbox")
				return
			}
			it.Name, it.Subject, it.Category, it.Priority, it.Source = deref(name), deref(subject), deref(category), deref(priority), deref(source)
			items = append(items, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			slog.Error("admin inbox: rows failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to load the inbox")
			return
		}

		var open int
		if err := pool.QueryRow(r.Context(), `
			SELECT (SELECT count(*) FROM public.support_requests WHERE resolved_at IS NULL)
			     + (SELECT count(*) FROM public.contact_requests WHERE resolved_at IS NULL)`).Scan(&open); err != nil {
			slog.Error("admin inbox: count failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to load the inbox")
			return
		}
		w.Header().Set("Cache-Control", "no-store") // personal data
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "open": open, "limit": limit})
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// inboxTable maps the {kind} URL segment to its table; nothing else reaches SQL.
func inboxTable(kind string) (string, bool) {
	switch kind {
	case "support":
		return "public.support_requests", true
	case "contact":
		return "public.contact_requests", true
	}
	return "", false
}

const inboxNoteMax = 2000

// AdminResolveInboxItem serves POST /platform/admin/inbox/{kind}/{id}/resolve
// with an optional {"note": "..."}: marks the message handled. Without a
// note an earlier one is kept (a message reopened and resolved again).
// Audited.
func AdminResolveInboxItem(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		table, id, ok := inboxTarget(w, r)
		if !ok {
			return
		}
		var body struct {
			Note string `json:"note"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid request body")
				return
			}
		}
		if utf8.RuneCountInString(body.Note) > inboxNoteMax {
			writeJSONError(w, http.StatusBadRequest, "note is too long (2000 characters at most)")
			return
		}
		actorID, actorEmail := audit.ActorFromContext(r.Context())
		var resolvedBy *string
		if _, err := uuid.Parse(actorID); err == nil {
			resolvedBy = &actorID
		}
		var note *string
		if body.Note != "" {
			note = &body.Note
		}
		var resolvedAt time.Time
		err := pool.QueryRow(r.Context(),
			`UPDATE `+table+` SET resolved_at = now(), resolved_by = $2,
			        resolution_note = COALESCE($3, resolution_note)
			  WHERE id = $1 RETURNING resolved_at`, id, resolvedBy, note).Scan(&resolvedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "message not found")
			return
		}
		if err != nil {
			slog.Error("admin inbox: resolve failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to update the message")
			return
		}
		logInboxAudit(r, actorID, actorEmail, "inbox.resolved", chi.URLParam(r, "kind"), id)
		writeJSON(w, http.StatusOK, map[string]any{"resolved_at": resolvedAt})
	}
}

// AdminReopenInboxItem serves DELETE /platform/admin/inbox/{kind}/{id}/resolve:
// marks the message open again (the note is kept). Audited.
func AdminReopenInboxItem(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		table, id, ok := inboxTarget(w, r)
		if !ok {
			return
		}
		tag, err := pool.Exec(r.Context(),
			`UPDATE `+table+` SET resolved_at = NULL, resolved_by = NULL WHERE id = $1`, id)
		if err != nil {
			slog.Error("admin inbox: reopen failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to update the message")
			return
		}
		if tag.RowsAffected() == 0 {
			writeJSONError(w, http.StatusNotFound, "message not found")
			return
		}
		actorID, actorEmail := audit.ActorFromContext(r.Context())
		logInboxAudit(r, actorID, actorEmail, "inbox.reopened", chi.URLParam(r, "kind"), id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func inboxTarget(w http.ResponseWriter, r *http.Request) (table, id string, ok bool) {
	table, ok = inboxTable(chi.URLParam(r, "kind"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown message kind")
		return "", "", false
	}
	id = chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeJSONError(w, http.StatusNotFound, "message not found")
		return "", "", false
	}
	return table, id, true
}

func logInboxAudit(r *http.Request, actorID, actorEmail, action, kind, id string) {
	svc := audit.FromContext(r.Context())
	if svc == nil {
		return
	}
	svc.Log(r.Context(), "", actorID, actorEmail, action,
		audit.WithTarget(kind+"_request", id),
		audit.WithIP(r.RemoteAddr))
}
