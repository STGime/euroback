package tenant

import (
	"context"
	"encoding/json"
	"html"
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
	SendServiceNotice(ctx context.Context, msgs []email.BroadcastMessage, subject string) (email.BulkResult, error)
}

// serviceNoticeAudienceSQL lists the accounts a service notice goes to:
// every verified, not suspended account, whatever its product-update
// preferences (a notice about the contract isn't marketing). Reserved
// and bounced addresses are still skipped. One row per address.
const serviceNoticeAudienceSQL = `
	SELECT DISTINCT ON (lower(trim(email))) lower(trim(email))
	  FROM public.platform_users
	 WHERE email_confirmed_at IS NOT NULL
	   AND suspended_at IS NULL
	   AND ` + reservedEmailFilterSQL + `
	 ORDER BY lower(trim(email))`

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
		if req.ExpectedRecipients != len(recipients) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{
				"error":      "the number of recipients changed; check it and send again",
				"recipients": len(recipients),
			})
			return
		}

		body := withServiceNoticeFooter(req.BodyHTML)
		msgs := make([]email.BroadcastMessage, 0, len(recipients))
		for _, e := range recipients {
			msgs = append(msgs, email.BroadcastMessage{To: e, HTMLBody: body})
		}

		auditSvc := audit.FromContext(r.Context())
		actorID, actorEmail := audit.ActorFromContext(r.Context())
		remoteAddr := r.RemoteAddr
		bgCtx := context.WithoutCancel(r.Context())
		subject := req.Subject
		count := len(msgs)
		runBroadcast(func() {
			ctx, cancel := context.WithTimeout(bgCtx, broadcastSendBudget)
			defer cancel()
			result, err := mailer.SendServiceNotice(ctx, msgs, subject)
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
		json.NewEncoder(w).Encode(map[string]any{"status": "queued", "recipients": count})
	}
}

// withServiceNoticeFooter adds a footer saying why the recipient gets
// this message although they may have turned off product updates.
func withServiceNoticeFooter(body string) string {
	const a = ` style="color:#6b7280; text-decoration:underline;"`
	text := `You get this service notice because you have a Eurobase account. It is about your contract with us, so it is sent whatever your mail preferences. Questions: <a href="mailto:` + html.EscapeString("contact@eurobase.app") + `"` + a + `>contact@eurobase.app</a>.`
	footer := `<p style="margin:16px auto 24px; max-width:600px; padding:0 16px; font-family:-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; font-size:12px; line-height:1.6; color:#9ca3af; text-align:center;">` +
		text + `</p>`
	if i := strings.LastIndex(strings.ToLower(body), "</body>"); i >= 0 {
		return body[:i] + footer + "\n" + body[i:]
	}
	return body + "\n" + footer
}
