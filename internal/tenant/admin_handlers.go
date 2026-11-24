package tenant

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/email"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BulkEmailer is the subset of internal/email.EmailService that admin
// broadcast needs. Returns a BulkResult so the handler can surface
// per-chunk success/failure to the console after a chunked TEM send
// (closes #35).
type BulkEmailer interface {
	SendBroadcast(ctx context.Context, msgs []email.BroadcastMessage, subject string) (email.BulkResult, error)
}

// UnsubscribeURLFunc returns an account's signed unsubscribe link for
// product updates ("" when unsubscribe links aren't configured).
type UnsubscribeURLFunc func(userID string) string

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
	// End of the Team beta window (000159); nil = no limit.
	BetaEndsAt    *time.Time `json:"beta_ends_at,omitempty"`
	BetaExpiredAt *time.Time `json:"beta_expired_at,omitempty"`
}

// AdminListAllProjects lists every project across every tenant. Gated by
// superadminMiddleware upstream, so this handler assumes the caller is
// already authorized.
func AdminListAllProjects(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(),
			`SELECT p.id, p.name, p.slug, p.schema_name, COALESCE(p.plan, 'free'),
			        COALESCE(p.status, 'active'), p.owner_id, u.email, p.created_at,
			        p.beta_ends_at, p.beta_expired_at
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
			if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.SchemaName, &p.Plan, &p.Status, &p.OwnerID, &p.OwnerEmail, &p.CreatedAt, &p.BetaEndsAt, &p.BetaExpiredAt); err != nil {
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
	Email     string    `json:"email"`
	Note      *string   `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
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

// AdminSendAllowlistEmail sends a single HTML email to one or more
// allowlist recipients. With two or more recipients the send uses BCC
// so each recipient sees only themselves in the visible headers —
// important when invitation lists shouldn't be exposed (e.g. beta
// testers learning who else is in the cohort). All recipients are
// validated against the live platform_allowlist table so a compromised
// superadmin token can't use this as a generic mail relay.
func AdminSendAllowlistEmail(pool *pgxpool.Pool, mailer BulkEmailer, unsubscribeURL UnsubscribeURLFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mailer == nil {
			http.Error(w, `{"error":"email service not configured"}`, http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Emails   []string `json:"emails"`
			Subject  string   `json:"subject"`
			BodyHTML string   `json:"body_html"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}

		// Normalise + dedupe.
		seen := make(map[string]struct{}, len(req.Emails))
		recipients := make([]string, 0, len(req.Emails))
		for _, e := range req.Emails {
			e = strings.ToLower(strings.TrimSpace(e))
			if e == "" || !strings.Contains(e, "@") {
				continue
			}
			if _, dup := seen[e]; dup {
				continue
			}
			seen[e] = struct{}{}
			recipients = append(recipients, e)
		}
		if len(recipients) == 0 {
			http.Error(w, `{"error":"no valid recipients"}`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Subject) == "" {
			http.Error(w, `{"error":"subject is required"}`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.BodyHTML) == "" {
			http.Error(w, `{"error":"body_html is required"}`, http.StatusBadRequest)
			return
		}

		// Validate every recipient is either on the allowlist or has
		// a platform_users account — the widened audience for the
		// broadcast flow (was allowlist-only pre-widening). Same
		// security fence purpose: a compromised superadmin token
		// can't use this as a generic mail relay; recipients are
		// bounded to people with a pre-existing relationship. Also
		// tracks each recipient's source (allowlist / users / both)
		// so the audit log records who reached what.
		//
		// `lower(trim(email))` normalization matches
		// AdminListBroadcastAudience — round-trip is byte-consistent
		// between the audience listing and the send validation.
		type source struct {
			onAllowlist, hasAccount bool
			userID                  string // the account's id ("" without one)
		}
		validSet := make(map[string]source, len(recipients))
		// Same reserved-TLD filter as AdminListBroadcastAudience —
		// a malicious/buggy caller can't force a probe address
		// through even if the audience endpoint were somehow
		// bypassed. Also protects against the case where a probe
		// address is on the allowlist directly (rare but possible
		// if someone manually inserted one).
		rows, err := pool.Query(r.Context(),
			`SELECT lower(trim(email)) AS email,
			        BOOL_OR(source = 'allowlist') AS on_allowlist,
			        BOOL_OR(source = 'user')      AS has_account,
			        COALESCE(min(user_id), '')    AS user_id
			   FROM (
			       SELECT email, 'allowlist' AS source, NULL::text AS user_id
			         FROM public.platform_allowlist
			        WHERE `+reservedEmailFilterSQL+`
			          AND `+broadcastAllowlistConsentSQL+`
			       UNION ALL
			       SELECT email, 'user' AS source, id::text AS user_id
			         FROM public.platform_users
			        WHERE `+reservedEmailFilterSQL+`
			          AND `+broadcastUserConsentSQL+`
			   ) s
			   WHERE lower(trim(email)) = ANY($1::text[])
			   GROUP BY lower(trim(email))`,
			recipients,
		)
		if err != nil {
			slog.Error("broadcast recipient lookup failed", "error", err)
			http.Error(w, `{"error":"recipient lookup failed"}`, http.StatusInternalServerError)
			return
		}
		for rows.Next() {
			var e, userID string
			var onAllow, hasAcct bool
			if err := rows.Scan(&e, &onAllow, &hasAcct, &userID); err == nil {
				validSet[e] = source{onAllowlist: onAllow, hasAccount: hasAcct, userID: userID}
			}
		}
		rows.Close()
		// validSet is keyed by the DB-normalized form (`lower(trim)`),
		// so we look up recipients under the same normalization —
		// otherwise `Foo@X.com` in an API caller's request array
		// misses the `foo@x.com` DB row and gets silently dropped
		// even though it's a valid recipient. Also dedups a
		// duplicated request payload (belt: keeps the "each
		// exactly once" guarantee even if the caller's list
		// contained a case-variant collision).
		final := recipients[:0]
		seenNorm := make(map[string]struct{}, len(recipients))
		var allowlistOnly, usersOnly, both int
		for _, e := range recipients {
			norm := strings.ToLower(strings.TrimSpace(e))
			s, ok := validSet[norm]
			if !ok {
				continue
			}
			if _, dup := seenNorm[norm]; dup {
				continue
			}
			seenNorm[norm] = struct{}{}
			final = append(final, e)
			switch {
			case s.onAllowlist && s.hasAccount:
				both++
			case s.onAllowlist:
				allowlistOnly++
			case s.hasAccount:
				usersOnly++
			}
		}
		if len(final) == 0 {
			// Widened message: could be allowlist OR user; a "not on
			// allowlist" refusal would mislead an operator whose
			// intent was to reach a signed-up user only.
			http.Error(w, `{"error":"no valid recipients — every address must be on the allowlist or have a platform_users account, with a verified address and no opt-out from product updates"}`, http.StatusBadRequest)
			return
		}

		// One message per recipient (never BCC): each account gets its
		// own one-click unsubscribe link, in the footer and in the
		// List-Unsubscribe header.
		consoleURL := broadcastConsoleURL()
		msgs := make([]email.BroadcastMessage, 0, len(final))
		for _, e := range final {
			var link string
			if uid := validSet[strings.ToLower(strings.TrimSpace(e))].userID; uid != "" && unsubscribeURL != nil {
				link = unsubscribeURL(uid)
			}
			msgs = append(msgs, email.BroadcastMessage{
				To:             e,
				HTMLBody:       withBroadcastFooter(req.BodyHTML, consoleURL, link),
				UnsubscribeURL: link,
			})
		}
		// Sending one message per recipient takes longer than a request
		// may run (30 s), so it runs in the background on a context of
		// its own and the handler answers 202 right away. The outcome
		// (sent / failed, failing addresses) goes to the audit log and
		// the gateway log when it's done.
		auditSvc := audit.FromContext(r.Context())
		actorID, actorEmail := audit.ActorFromContext(r.Context())
		remoteAddr := r.RemoteAddr
		bgCtx := context.WithoutCancel(r.Context())
		subject := req.Subject
		skipped := len(recipients) - len(final)
		recipientCount := len(final)
		runBroadcast(func() {
			ctx, cancel := context.WithTimeout(bgCtx, broadcastSendBudget)
			defer cancel()
			result, err := mailer.SendBroadcast(ctx, msgs, subject)
			failedAddrs := make([]string, 0, len(result.Errors))
			for _, e := range result.Errors {
				failedAddrs = append(failedAddrs, e.Recipients...)
			}
			if err != nil {
				slog.Error("admin broadcast: nothing sent", "error", err, "count", recipientCount)
			} else {
				slog.Info("admin broadcast done", "sent", result.Sent, "failed", result.Failed, "subject", subject)
			}
			if len(failedAddrs) > 0 {
				slog.Warn("admin broadcast: failed recipients", "recipients", failedAddrs)
			}
			if auditSvc == nil {
				return
			}
			if len(failedAddrs) > 100 {
				failedAddrs = failedAddrs[:100]
			}
			// Log as ActionBroadcastEmailed (not ActionAllowlistEmailed)
			// now that the audience is widened. Legacy action constant
			// is retained in internal/audit/service.go for historical
			// row parsing. The three counters make it possible to
			// reconstruct the exact audience shape at send time —
			// useful when an operator later asks "who did we tell
			// about the release?"
			auditCtx, auditCancel := context.WithTimeout(bgCtx, 10*time.Second)
			defer auditCancel()
			auditSvc.Log(auditCtx, "", actorID, actorEmail, audit.ActionBroadcastEmailed,
				audit.WithMetadata(map[string]any{
					"recipient_count":      recipientCount,
					"skipped_count":        skipped,
					"allowlist_only_count": allowlistOnly,
					"users_only_count":     usersOnly,
					"both_count":           both,
					"sent":                 result.Sent,
					"failed":               result.Failed,
					"failed_recipients":    failedAddrs,
					"subject":              subject,
				}),
				audit.WithIP(remoteAddr))
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{
			"status":     "queued",
			"recipients": recipientCount,
			// Left out: bounced, reserved, unverified or opted out.
			"skipped": skipped,
		})
	}
}

// broadcastSendBudget bounds one background broadcast.
const broadcastSendBudget = 30 * time.Minute

// runBroadcast starts a broadcast send; tests replace it to run inline.
var runBroadcast = func(f func()) { go f() }

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
