package tenant

// Team beta window (migration 000159). Team / Legal Team projects are a
// free beta for TeamBetaDays from creation (projects.beta_ends_at, set by
// CreateProject; projects that existed before keep NULL = no limit). The
// worker runs TeamBetaExpiry hourly:
//
//   - TeamBetaReminderBefore the end, the owner gets one email
//     (beta_reminded_at).
//   - At the end the project is suspended (status 'suspended', data kept,
//     same effect as Admin → Projects → Suspend), beta_expired_at is set,
//     the owner gets an email and ops a Discord alert.
//
// A superadmin extends the window (AdminExtendTeamBeta); that also lifts a
// suspension the window caused. A suspension the window didn't cause is
// left alone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/mailaddr"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// TeamBetaDays is the length of the beta window, and of an extension.
	TeamBetaDays = 14
	// TeamBetaReminderBefore is when the owner is told the end is near.
	TeamBetaReminderBefore = 4 * 24 * time.Hour
)

// BetaMailer is the email surface the window needs (email.EmailService).
type BetaMailer interface {
	SendRaw(ctx context.Context, to, subject, htmlBody string) error
}

// TeamBetaExpiry sends reminders and suspends projects whose beta ended.
type TeamBetaExpiry struct {
	Pool        *pgxpool.Pool
	Mailer      BetaMailer // nil: no owner emails
	Audit       *audit.Service
	OpsWebhook  string // Discord webhook for the expiry alert; "" = none
	AdminURL    string // link in the alert
	ConsoleURL  string // link in the emails
	Now         func() time.Time
	SendDiscord func(webhookURL, what string, embed map[string]any) // tests; default postDiscordAsync
}

// Start runs the job at startup and then every interval until ctx ends.
func (t *TeamBetaExpiry) Start(ctx context.Context, interval time.Duration) {
	go func() {
		t.RunOnce(ctx)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				t.RunOnce(ctx)
			}
		}
	}()
}

type betaProject struct {
	id, name, slug, ownerEmail string
	endsAt                     time.Time
}

// RunOnce does one pass: reminders first, then expiries.
func (t *TeamBetaExpiry) RunOnce(ctx context.Context) {
	if t.Pool == nil {
		return
	}
	t.remind(ctx)
	t.expire(ctx)
}

func (t *TeamBetaExpiry) remind(ctx context.Context) {
	// Claimed in the UPDATE, so two workers never mail the same window twice.
	rows, err := t.Pool.Query(ctx, `
		UPDATE public.projects p
		   SET beta_reminded_at = now()
		  FROM public.platform_users u
		 WHERE u.id = p.owner_id
		   AND p.beta_ends_at IS NOT NULL
		   AND p.beta_reminded_at IS NULL
		   AND p.status = 'active'
		   AND p.beta_ends_at > now()
		   AND p.beta_ends_at <= now() + make_interval(secs => $1)
		RETURNING p.id, p.name, p.slug, u.email, p.beta_ends_at`,
		TeamBetaReminderBefore.Seconds())
	if err != nil {
		slog.Error("team beta: reminder query failed", "error", err)
		return
	}
	list, err := scanBetaProjects(rows)
	if err != nil {
		slog.Error("team beta: reminder scan failed", "error", err)
		return
	}
	for _, p := range list {
		subject, body := renderTeamBetaMail(p, false, t.ConsoleURL)
		t.mailOwner(ctx, p, subject, body)
		slog.Info("team beta: reminder sent", "project_id", p.id, "ends_at", p.endsAt)
	}
}

func (t *TeamBetaExpiry) expire(ctx context.Context) {
	rows, err := t.Pool.Query(ctx, `
		UPDATE public.projects p
		   SET status = 'suspended', beta_expired_at = now()
		  FROM public.platform_users u
		 WHERE u.id = p.owner_id
		   AND p.beta_ends_at IS NOT NULL
		   AND p.beta_ends_at <= now()
		   AND p.beta_expired_at IS NULL
		   AND p.status = 'active'
		RETURNING p.id, p.name, p.slug, u.email, p.beta_ends_at`)
	if err != nil {
		slog.Error("team beta: expiry update failed", "error", err)
		return
	}
	list, err := scanBetaProjects(rows)
	if err != nil {
		slog.Error("team beta: expiry scan failed", "error", err)
		return
	}
	for _, p := range list {
		if t.Audit != nil {
			t.Audit.Log(ctx, p.id, "", "system", audit.ActionProjectSuspended,
				audit.WithTarget("project", p.id),
				audit.WithMetadata(map[string]interface{}{"slug": p.slug, "reason": "Team beta ended"}))
		}
		subject, body := renderTeamBetaMail(p, true, t.ConsoleURL)
		t.mailOwner(ctx, p, subject, body)
		t.alertOps(p)
		slog.Warn("team beta: project suspended, beta ended", "project_id", p.id, "slug", p.slug)
	}
}

func scanBetaProjects(rows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}) ([]betaProject, error) {
	defer rows.Close()
	var out []betaProject
	for rows.Next() {
		var p betaProject
		if err := rows.Scan(&p.id, &p.name, &p.slug, &p.ownerEmail, &p.endsAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// mailOwner sends best-effort; reserved and suppressed addresses are
// skipped like other platform mail.
func (t *TeamBetaExpiry) mailOwner(ctx context.Context, p betaProject, subject, body string) {
	if t.Mailer == nil || p.ownerEmail == "" || mailaddr.IsReserved(p.ownerEmail) {
		return
	}
	var suppressed bool
	if err := t.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM public.email_suppressions WHERE email = lower($1))`,
		p.ownerEmail).Scan(&suppressed); err == nil && suppressed {
		slog.Info("team beta: owner address suppressed, no mail", "project_id", p.id)
		return
	}
	if err := t.Mailer.SendRaw(ctx, p.ownerEmail, subject, body); err != nil {
		slog.Warn("team beta: mail failed", "project_id", p.id, "error", err)
	}
}

// alertOps pings ops: project slug and id only, no personal data.
func (t *TeamBetaExpiry) alertOps(p betaProject) {
	if t.OpsWebhook == "" {
		return
	}
	send := t.SendDiscord
	if send == nil {
		send = postDiscordAsync
	}
	send(t.OpsWebhook, "team_beta", map[string]any{
		"title":       "⏳ Team beta ended, project suspended",
		"description": fmt.Sprintf("**%s** (`%s`): data kept. Extend in Admin → Projects to reactivate.", p.slug, p.id),
		"url":         t.AdminURL,
		"color":       0xf59e0b,
	})
}

func renderTeamBetaMail(p betaProject, ended bool, consoleURL string) (subject, body string) {
	name := html.EscapeString(p.name)
	// The subject is a header: no control characters from the project name.
	plain := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, p.name)
	when := p.endsAt.UTC().Format("2 January 2006")
	projectURL := strings.TrimRight(consoleURL, "/") + "/p/" + p.id

	subject = fmt.Sprintf("Eurobase — the Team beta for %s ends on %s", plain, when)
	heading := "Team beta ending"
	what := fmt.Sprintf(
		"The free Team beta for your project <strong>%s</strong> ends on <strong>%s</strong>. "+
			"After that the project is paused: your database, storage, functions and users are kept, but the API and console stop serving it until the beta is extended.", name, when)
	next := "If you'd like to keep evaluating, or to talk about moving to a paid Team plan, write to " +
		`<a href="mailto:contact@eurobase.app" style="color:#1d4ed8;">contact@eurobase.app</a>. ` +
		`You can also export your data any time before then: <a href="` + projectURL + `" style="color:#1d4ed8;">open the project</a> and go to Compliance → Data Export.`
	if ended {
		subject = fmt.Sprintf("Eurobase — the Team beta for %s has ended", plain)
		heading = "Team beta ended"
		what = fmt.Sprintf(
			"The free Team beta for your project <strong>%s</strong> ended on <strong>%s</strong>, so the project is now paused. "+
				"Nothing is deleted: your database, storage, functions and users are kept.", name, when)
		next = "To continue — a longer evaluation or a paid Team plan — write to " +
			`<a href="mailto:contact@eurobase.app" style="color:#1d4ed8;">contact@eurobase.app</a>, and we'll reactivate the project.`
	}

	body = fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<body style="margin:0;padding:0;background-color:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#f3f4f6;padding:24px 0;">
    <tr><td align="center">
      <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;background:#ffffff;border-radius:12px;">
        <tr><td style="background:#1d4ed8;padding:24px 32px;color:#fff;">
          <p style="margin:0;font-size:20px;font-weight:700;">Eurobase</p>
          <p style="margin:6px 0 0;font-size:14px;color:#bfdbfe;">%s</p>
        </td></tr>
        <tr><td style="padding:32px;color:#111827;">
          <p style="margin:0 0 16px;font-size:16px;">Hi,</p>
          <p style="margin:0 0 16px;font-size:15px;line-height:1.6;color:#374151;">%s</p>
          <p style="margin:0 0 16px;font-size:14px;line-height:1.6;color:#374151;">%s</p>
          <p style="margin:20px 0 6px;font-size:14px;color:#374151;">Thanks,<br><strong>Stefan</strong></p>
        </td></tr>
        <tr><td style="padding:20px 32px 28px;border-top:1px solid #e5e7eb;">
          <p style="margin:0;font-size:12px;color:#9ca3af;">
            Eurobase O&Uuml;, Ahtri 12, Tallinn 15551, Estonia &middot; <a href="https://www.eurobase.app" style="color:#6b7280;">eurobase.app</a>
          </p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`, heading, what, next)
	return subject, body
}

// AdminExtendTeamBeta moves a project's Team beta end to
// max(end, now) + TeamBetaDays (superadmin only), so the owner gets a new
// reminder. If the window had suspended the project, it is reactivated;
// a suspension for any other reason is left alone. Projects without a
// beta end (created before 000159, or not Team) answer 409.
func AdminExtendTeamBeta(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := strings.TrimSpace(chi.URLParam(r, "id"))
		if _, err := uuid.Parse(projectID); err != nil {
			http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
			return
		}
		var slug, status string
		var endsAt time.Time
		var reactivated bool
		err := pool.QueryRow(r.Context(), `
			WITH old AS (
			    SELECT id, status = 'suspended' AND beta_expired_at IS NOT NULL AS by_window
			      FROM public.projects
			     WHERE id = $1::uuid AND beta_ends_at IS NOT NULL
			       FOR UPDATE
			)
			UPDATE public.projects p
			   SET beta_ends_at     = GREATEST(p.beta_ends_at, now()) + make_interval(days => $2),
			       beta_reminded_at = NULL,
			       beta_expired_at  = NULL,
			       status           = CASE WHEN old.by_window THEN 'active' ELSE p.status END
			  FROM old
			 WHERE p.id = old.id
			RETURNING p.slug, p.status, p.beta_ends_at, old.by_window`,
			projectID, TeamBetaDays).Scan(&slug, &status, &endsAt, &reactivated)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, `{"error":"project has no Team beta end date","code":"no_beta_window"}`, http.StatusConflict)
			return
		}
		if err != nil {
			slog.Error("team beta: extend failed", "project_id", projectID, "error", err)
			http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
			return
		}
		if svc := audit.FromContext(r.Context()); svc != nil {
			actorID, actorEmail := audit.ActorFromContext(r.Context())
			svc.Log(r.Context(), projectID, actorID, actorEmail, audit.ActionProjectBetaExtended,
				audit.WithTarget("project", projectID),
				audit.WithMetadata(map[string]any{"slug": slug, "beta_ends_at": endsAt.UTC().Format(time.RFC3339)}),
				audit.WithIP(r.RemoteAddr))
			if reactivated {
				svc.Log(r.Context(), projectID, actorID, actorEmail, audit.ActionProjectUnsuspended,
					audit.WithTarget("project", projectID),
					audit.WithMetadata(map[string]any{"slug": slug, "reason": "Team beta extended"}),
					audit.WithIP(r.RemoteAddr))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": projectID, "status": status, "beta_ends_at": endsAt, "reactivated": reactivated,
		})
	}
}
