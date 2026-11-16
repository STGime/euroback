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

// BlockedDomainEntry mirrors a public.platform_blocked_signup_domains row.
// It is the admin-managed signup blocklist: PlatformAuthService.SignUp
// refuses an address whose domain (or a parent domain) is listed.
type BlockedDomainEntry struct {
	Domain    string    `json:"domain"`
	Reason    *string   `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// normalizeBlockedDomain lowercases, trims, and strips a leading "@" or
// dots from a domain the admin typed, then validates it looks like a
// registrable domain or subdomain. Returns ("", false) when it doesn't.
func normalizeBlockedDomain(raw string) (string, bool) {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(d, "@") // tolerate a pasted "@domain"
	d = strings.Trim(d, ".")       // no leading/trailing dots
	if d == "" || strings.ContainsAny(d, "@ \t/") {
		return "", false
	}
	// Must have at least one dot (a bare TLD like "com" is too broad to
	// ever be a real blocklist entry and would knock out huge swathes).
	if !strings.Contains(d, ".") {
		return "", false
	}
	return d, true
}

// AdminListBlockedDomains returns every signup-blocklist entry.
func AdminListBlockedDomains(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(),
			`SELECT domain, reason, created_at
			   FROM public.platform_blocked_signup_domains
			  ORDER BY created_at DESC`)
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]BlockedDomainEntry, 0)
		for rows.Next() {
			var e BlockedDomainEntry
			if err := rows.Scan(&e.Domain, &e.Reason, &e.CreatedAt); err != nil {
				http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
				return
			}
			out = append(out, e)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"entries": out, "total": len(out)})
	}
}

// AdminAddBlockedDomain upserts a signup-blocklist entry.
func AdminAddBlockedDomain(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domain string `json:"domain"`
			Reason string `json:"reason,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		domain, ok := normalizeBlockedDomain(req.Domain)
		if !ok {
			http.Error(w, `{"error":"a valid domain is required (e.g. example.com)"}`, http.StatusBadRequest)
			return
		}

		if _, err := pool.Exec(r.Context(),
			`INSERT INTO public.platform_blocked_signup_domains (domain, reason)
			 VALUES ($1, NULLIF($2, ''))
			 ON CONFLICT (domain) DO UPDATE SET reason = EXCLUDED.reason`,
			domain, strings.TrimSpace(req.Reason)); err != nil {
			http.Error(w, `{"error":"insert failed"}`, http.StatusInternalServerError)
			return
		}

		if svc := audit.FromContext(r.Context()); svc != nil {
			actorID, actorEmail := audit.ActorFromContext(r.Context())
			svc.Log(r.Context(), "", actorID, actorEmail, audit.ActionBlockedDomainAdded,
				audit.WithTarget("blocked_domain", domain),
				audit.WithMetadata(map[string]any{"domain": domain, "reason": req.Reason}),
				audit.WithIP(r.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "added", "domain": domain})
	}
}

// AdminRemoveBlockedDomain deletes a signup-blocklist entry by domain.
func AdminRemoveBlockedDomain(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		domain, ok := normalizeBlockedDomain(chi.URLParam(r, "domain"))
		if !ok {
			http.Error(w, `{"error":"a valid domain is required"}`, http.StatusBadRequest)
			return
		}
		tag, err := pool.Exec(r.Context(),
			`DELETE FROM public.platform_blocked_signup_domains WHERE domain = $1`, domain)
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
			svc.Log(r.Context(), "", actorID, actorEmail, audit.ActionBlockedDomainRemoved,
				audit.WithTarget("blocked_domain", domain),
				audit.WithIP(r.RemoteAddr))
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
