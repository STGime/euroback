package tenant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/email"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ServiceNoticeEmailer is the subset of email.EmailService a service
// notice needs.
type ServiceNoticeEmailer interface {
	SendServiceNotice(ctx context.Context, msgs []email.BroadcastMessage, subject string, onResult func(to, messageID string, err error)) (email.BulkResult, error)
}

// serviceNoticeAudienceSQL lists the accounts a service notice goes to:
// every verified, not suspended account, whatever its product-update
// preferences (a notice about the contract isn't marketing). Reserved
// and bounced addresses (email_suppressions, and the platform sender's
// own email_bounce_blocks, keyed like mailaddr.Hash) are still skipped.
// One row per address.
const serviceNoticeAudienceSQL = `
	SELECT DISTINCT lower(trim(email))
	  FROM public.platform_users
	 WHERE email_confirmed_at IS NOT NULL
	   AND suspended_at IS NULL
	   AND ` + reservedEmailFilterSQL + `
	   AND NOT EXISTS (SELECT 1 FROM public.email_bounce_blocks b
	                    WHERE b.address_hash = encode(sha256(convert_to(lower(trim(email)), 'UTF8')), 'hex')
	                      AND b.blocked_until > now())
	 ORDER BY 1`

// serviceNoticeMaxRecipients is a sanity cap: a larger audience means
// something is wrong with the query, so the send refuses loudly.
const serviceNoticeMaxRecipients = 20000

// noticeKey identifies a notice by its subject, so sending the same
// notice again resumes it instead of mailing everyone twice.
func noticeKey(subject string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(subject)))
	return hex.EncodeToString(h[:])
}

// alreadySent returns the addresses that got the notice with this key.
func alreadySent(ctx context.Context, pool *pgxpool.Pool, key string) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT email FROM public.service_notice_deliveries WHERE notice_key = $1 AND status = 'sent'`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out[e] = true
	}
	return out, rows.Err()
}

func serviceNoticeAudience(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, serviceNoticeAudienceSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AdminServiceNoticeAudience answers GET /platform/admin/service-notice/audience
// with the number of accounts a service notice would reach.
func AdminServiceNoticeAudience(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := serviceNoticeAudience(r.Context(), pool)
		if err != nil {
			slog.Error("service notice audience query failed", "error", err)
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"recipients": len(list)})
	}
}

// AdminSendServiceNotice answers POST /platform/admin/service-notice: it
// sends subject + body_html to every account in the service-notice
// audience, one message each, in the background (202). The caller passes
// the recipient count it was shown (expected_recipients); a different
// count (someone signed up meanwhile) is refused with 409 and the new
// count, so the operator confirms what is actually sent. Audited as
// broadcast.emailed with kind "service_notice".
func AdminSendServiceNotice(pool *pgxpool.Pool, mailer ServiceNoticeEmailer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mailer == nil {
			http.Error(w, `{"error":"email service not configured"}`, http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Subject            string `json:"subject"`
			BodyHTML           string `json:"body_html"`
			ExpectedRecipients int    `json:"expected_recipients"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Subject) == "" || strings.TrimSpace(req.BodyHTML) == "" {
			http.Error(w, `{"error":"subject and body_html are required"}`, http.StatusBadRequest)
			return
		}
		recipients, err := serviceNoticeAudience(r.Context(), pool)
		if err != nil {
			slog.Error("service notice audience query failed", "error", err)
			http.Error(w, `{"error":"recipient lookup failed"}`, http.StatusInternalServerError)
			return
		}
		if len(recipients) == 0 {
			http.Error(w, `{"error":"no recipients"}`, http.StatusBadRequest)
			return
		}
		if len(recipients) > serviceNoticeMaxRecipients {
			slog.Error("service notice: audience larger than the cap; refusing", "count", len(recipients))
			http.Error(w, `{"error":"audience larger than expected; nothing sent"}`, http.StatusInternalServerError)
			return
		}
		if req.ExpectedRecipients != len(recipients) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{
				"error":      "the number of recipients changed; check it and send again",
				"recipients": len(recipients),
			})
			return
		}

		// A notice already sent (same subject) only goes to the addresses
		// that haven't got it yet: a second click, a retry or a send cut
		// off by a restart resumes instead of mailing everyone twice.
		key := noticeKey(req.Subject)
		done, err := alreadySent(r.Context(), pool, key)
		if err != nil {
			slog.Error("service notice: delivery lookup failed", "error", err)
			http.Error(w, `{"error":"delivery lookup failed"}`, http.StatusInternalServerError)
			return
		}
		body := withServiceNoticeFooter(req.BodyHTML)
		msgs := make([]email.BroadcastMessage, 0, len(recipients))
		for _, e := range recipients {
			if !done[e] {
				msgs = append(msgs, email.BroadcastMessage{To: e, HTMLBody: body})
			}
		}
		if len(msgs) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{
				"error":        "every recipient already got this notice",
				"already_sent": len(done),
			})
			return
		}

		auditSvc := audit.FromContext(r.Context())
		actorID, actorEmail := audit.ActorFromContext(r.Context())
		remoteAddr := r.RemoteAddr
		bgCtx := context.WithoutCancel(r.Context())
		subject := req.Subject
		count := len(msgs)
		skipped := len(recipients) - count
		// Recorded before sending, so a send cut off by a restart still
		// leaves a trace in the audit log.
		if auditSvc != nil {
			auditSvc.Log(r.Context(), "", actorID, actorEmail, audit.ActionBroadcastEmailed,
				audit.WithMetadata(map[string]any{
					"kind": "service_notice", "stage": "started", "notice_key": key,
					"recipient_count": count, "already_sent": skipped, "subject": subject,
				}),
				audit.WithIP(remoteAddr))
		}
		record := func(to, messageID string, sendErr error) {
			status, errText := "sent", ""
			if sendErr != nil {
				status, errText = "failed", sendErr.Error()
				if len(errText) > 500 {
					errText = errText[:500]
				}
			}
			rctx, rcancel := context.WithTimeout(bgCtx, 5*time.Second)
			defer rcancel()
			if _, err := pool.Exec(rctx, `
				INSERT INTO public.service_notice_deliveries (notice_key, subject, email, status, message_id, error)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''))
				ON CONFLICT (notice_key, email) DO UPDATE
				   SET status = EXCLUDED.status, message_id = EXCLUDED.message_id,
				       error = EXCLUDED.error, attempted_at = now()`,
				key, subject, to, status, messageID, errText); err != nil {
				slog.Error("service notice: recording delivery failed", "error", err, "status", status)
			}
		}
		runBroadcast(func() {
			ctx, cancel := context.WithTimeout(bgCtx, broadcastSendBudget)
			defer cancel()
			result, err := mailer.SendServiceNotice(ctx, msgs, subject, record)
			failed := make([]string, 0, len(result.Errors))
			for _, e := range result.Errors {
				failed = append(failed, e.Recipients...)
			}
			if err != nil {
				slog.Error("service notice: nothing sent", "error", err, "count", count)
			} else {
				slog.Info("service notice done", "sent", result.Sent, "failed", result.Failed, "subject", subject)
			}
			if len(failed) > 0 {
				slog.Warn("service notice: failed recipients", "recipients", failed)
			}
			if auditSvc == nil {
				return
			}
			if len(failed) > 100 {
				failed = failed[:100]
			}
			auditCtx, auditCancel := context.WithTimeout(bgCtx, 10*time.Second)
			defer auditCancel()
			auditSvc.Log(auditCtx, "", actorID, actorEmail, audit.ActionBroadcastEmailed,
				audit.WithMetadata(map[string]any{
					"kind":              "service_notice",
					"stage":             "done",
					"notice_key":        key,
					"recipient_count":   count,
					"sent":              result.Sent,
					"failed":            result.Failed,
					"failed_recipients": failed,
					"subject":           subject,
				}),
				audit.WithIP(remoteAddr))
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"status": "queued", "recipients": count, "already_sent": skipped})
	}
}

// withServiceNoticeFooter adds a footer saying why the recipient gets
// this message although they may have turned off product updates.
func withServiceNoticeFooter(body string) string {
	const a = ` style="color:#6b7280; text-decoration:underline;"`
	text := `You get this service notice because you have a Eurobase account. It is about your contract with us, so it is sent whatever your mail preferences. Questions: <a href="mailto:contact@eurobase.app"` + a + `>contact@eurobase.app</a>.`
	footer := `<p style="margin:16px auto 24px; max-width:600px; padding:0 16px; font-family:-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; font-size:12px; line-height:1.6; color:#9ca3af; text-align:center;">` +
		text + `</p>`
	if i := strings.LastIndex(strings.ToLower(body), "</body>"); i >= 0 {
		return body[:i] + footer + "\n" + body[i:]
	}
	return body + "\n" + footer
}
