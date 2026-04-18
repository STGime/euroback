package tenant

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminProject is a platform-wide project row returned to superadmins.
type AdminProject struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	SchemaName string    `json:"schema_name"`
	Plan       string    `json:"plan"`
	Status     string    `json:"status"`
	OwnerID    string    `json:"owner_id"`
	OwnerEmail string    `json:"owner_email"`
	CreatedAt  time.Time `json:"created_at"`
}

// AdminListAllProjects lists every project across every tenant. Gated by
// superadminMiddleware upstream, so this handler assumes the caller is
// already authorized.
func AdminListAllProjects(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(),
			`SELECT p.id, p.name, p.slug, p.schema_name, COALESCE(p.plan, 'free'),
			        COALESCE(p.status, 'active'), p.owner_id, u.email, p.created_at
			 FROM public.projects p
			 LEFT JOIN public.platform_users u ON u.id = p.owner_id
			 ORDER BY p.created_at DESC
			 LIMIT 500`)
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]AdminProject, 0)
		for rows.Next() {
			var p AdminProject
			if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.SchemaName, &p.Plan, &p.Status, &p.OwnerID, &p.OwnerEmail, &p.CreatedAt); err != nil {
				http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
				return
			}
			out = append(out, p)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"projects": out, "total": len(out)})
	}
}

// AllowlistEntry mirrors the platform_allowlist row.
type AllowlistEntry struct {
	Email     string     `json:"email"`
	Note      *string    `json:"note,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// AdminListAllowlist returns all platform_allowlist entries.
func AdminListAllowlist(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(),
			`SELECT email, note, created_at FROM public.platform_allowlist ORDER BY created_at DESC`)
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]AllowlistEntry, 0)
		for rows.Next() {
			var e AllowlistEntry
			if err := rows.Scan(&e.Email, &e.Note, &e.CreatedAt); err != nil {
				http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
				return
			}
			out = append(out, e)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"entries": out, "total": len(out)})
	}
}

// AdminAddAllowlist upserts a platform_allowlist entry.
func AdminAddAllowlist(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email string `json:"email"`
			Note  string `json:"note,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email == "" || !strings.Contains(email, "@") {
			http.Error(w, `{"error":"valid email is required"}`, http.StatusBadRequest)
			return
		}

		_, err := pool.Exec(r.Context(),
			`INSERT INTO public.platform_allowlist (email, note)
			 VALUES ($1, NULLIF($2, ''))
			 ON CONFLICT (email) DO UPDATE SET note = EXCLUDED.note`,
			email, req.Note)
		if err != nil {
			http.Error(w, `{"error":"insert failed"}`, http.StatusInternalServerError)
			return
		}

		if svc := audit.FromContext(r.Context()); svc != nil {
			actorID, actorEmail := audit.ActorFromContext(r.Context())
			svc.Log(r.Context(), "", actorID, actorEmail, audit.ActionAllowlistAdded,
				audit.WithTarget("allowlist_email", email),
				audit.WithMetadata(map[string]any{"email": email, "note": req.Note}),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "added", "email": email})
	}
}

// AdminRemoveAllowlist deletes a platform_allowlist entry by email.
func AdminRemoveAllowlist(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "email")))
		if email == "" {
			http.Error(w, `{"error":"email is required"}`, http.StatusBadRequest)
			return
		}
		tag, err := pool.Exec(r.Context(),
			`DELETE FROM public.platform_allowlist WHERE email = $1`, email)
		if err != nil {
			http.Error(w, `{"error":"delete failed"}`, http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}

		if svc := audit.FromContext(r.Context()); svc != nil {
			actorID, actorEmail := audit.ActorFromContext(r.Context())
			svc.Log(r.Context(), "", actorID, actorEmail, audit.ActionAllowlistRemoved,
				audit.WithTarget("allowlist_email", email),
				audit.WithIP(r.RemoteAddr))
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

