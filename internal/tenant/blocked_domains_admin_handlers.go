package tenant

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BlockedDomainEntry mirrors a public.platform_blocked_signup_domains row.
// It is the admin-managed signup blocklist: PlatformAuthService.SignUp
// refuses an address whose domain (or a parent domain) is listed.
type BlockedDomainEntry struct {
	Domain    string    `json:"domain"`
	Reason    *string   `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Source: "manual" (an admin added it) or "disposable_list" (the
	// weekly sync, workers.StartDisposableDomainSync, added it).
	Source string `json:"source"`
}

// BlockedDomainSyncStatus is the last run of the disposable-domain list
// sync (public.platform_blocked_domain_sync).
type BlockedDomainSyncStatus struct {
	LastRunAt *time.Time `json:"last_run_at"`
	LastOKAt  *time.Time `json:"last_ok_at"`
	ListSize  *int       `json:"list_size"`
	Added     *int       `json:"added"`
	Removed   *int       `json:"removed"`
	Error     *string    `json:"error,omitempty"`
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

// Page size for the blocked-domains list. The list can hold thousands of
// rows (a bulk-loaded disposable-email list), so it is paginated.
const (
	blockedDomainsDefaultLimit = 50
	blockedDomainsMaxLimit     = 200
)

// blockedDomainsQuery reads ?q= (substring of the domain), ?limit= and
// ?offset= for the blocked-domains list, clamped to sane values.
func blockedDomainsQuery(r *http.Request) (q string, limit, offset int) {
	v := r.URL.Query()
	// Postgres refuses invalid UTF-8 and NUL in a text parameter (a 500);
	// drop both and cut on a rune boundary.
	q = strings.ToValidUTF8(v.Get("q"), "")
	q = strings.ReplaceAll(q, "\x00", "")
	q = strings.ToLower(strings.TrimSpace(q))
	if r := []rune(q); len(r) > 253 {
		q = string(r[:253])
	}
	limit = blockedDomainsDefaultLimit
	if n, err := strconv.Atoi(v.Get("limit")); err == nil && n > 0 {
		limit = min(n, blockedDomainsMaxLimit)
	}
	if n, err := strconv.Atoi(v.Get("offset")); err == nil && n > 0 {
		offset = n
	}
	return q, limit, offset
}

// escapeLikePattern escapes LIKE's wildcards (and its escape character) so
// a search matches the text literally.
func escapeLikePattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// AdminListBlockedDomains returns one page of signup-blocklist entries,
// newest first, optionally filtered by a substring of the domain.
// Response: {entries, total (matching rows), limit, offset, sync}.
func AdminListBlockedDomains(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, limit, offset := blockedDomainsQuery(r)
		pattern := "%" + escapeLikePattern(q) + "%"

		var total int
		if err := pool.QueryRow(r.Context(),
			`SELECT count(*) FROM public.platform_blocked_signup_domains WHERE domain LIKE $1`,
			pattern).Scan(&total); err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		rows, err := pool.Query(r.Context(),
			`SELECT domain, reason, created_at, source
			   FROM public.platform_blocked_signup_domains
			  WHERE domain LIKE $1
			  ORDER BY created_at DESC, domain
			  LIMIT $2 OFFSET $3`,
			pattern, limit, offset)
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		out := make([]BlockedDomainEntry, 0, limit)
		for rows.Next() {
			var e BlockedDomainEntry
			if err := rows.Scan(&e.Domain, &e.Reason, &e.CreatedAt, &e.Source); err != nil {
				http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
				return
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		var sync BlockedDomainSyncStatus
		if err := pool.QueryRow(r.Context(),
			`SELECT last_run_at, last_ok_at, list_size, added, removed, error
			   FROM public.platform_blocked_domain_sync WHERE id = 1`).Scan(
			&sync.LastRunAt, &sync.LastOKAt, &sync.ListSize, &sync.Added, &sync.Removed, &sync.Error); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"entries": out, "total": total, "limit": limit, "offset": offset, "sync": sync,
		})
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

		// A domain blocked by hand is "manual" (the list sync never removes
		// it), and no longer excluded from the list.
		if err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(r.Context(),
				`INSERT INTO public.platform_blocked_signup_domains (domain, reason, source)
				 VALUES ($1, NULLIF($2, ''), 'manual')
				 ON CONFLICT (domain) DO UPDATE SET reason = EXCLUDED.reason, source = 'manual'`,
				domain, strings.TrimSpace(req.Reason)); err != nil {
				return err
			}
			_, err := tx.Exec(r.Context(),
				`DELETE FROM public.platform_blocked_domain_exclusions WHERE domain = $1`, domain)
			return err
		}); err != nil {
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
		// Unblocking a domain that came from the disposable list records an
		// exclusion, so the weekly sync doesn't add it back.
		found := false
		if err := pgx.BeginFunc(r.Context(), pool, func(tx pgx.Tx) error {
			var source string
			err := tx.QueryRow(r.Context(),
				`DELETE FROM public.platform_blocked_signup_domains WHERE domain = $1 RETURNING source`,
				domain).Scan(&source)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			found = true
			if source == "disposable_list" {
				_, err = tx.Exec(r.Context(),
					`INSERT INTO public.platform_blocked_domain_exclusions (domain) VALUES ($1)
					 ON CONFLICT (domain) DO NOTHING`, domain)
			}
			return err
		}); err != nil {
			http.Error(w, `{"error":"delete failed"}`, http.StatusInternalServerError)
			return
		}
		if !found {
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
