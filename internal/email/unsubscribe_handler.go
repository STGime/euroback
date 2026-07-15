package email

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UnsubscribeHandler handles the opt-out endpoint that lives in
// every outbound platform-mail footer. GET-only (idempotent — matches
// what mail clients + bots will do to preview the link).
//
// URL shape: GET /platform/mailing/unsubscribe?token=<opaque>
//
// Flow:
//   1. Verify the token via UnsubscribeSigner (HMAC + expiry).
//   2. UPSERT mailing_preferences (user_id, category, opted_out_at=now()).
//   3. Render a confirmation HTML page with a resubscribe button.
//
// Nothing here is user-authenticated: possession of a signed token
// IS the authorisation. The token is user-scoped + expiring, so a
// leaked link only opts out that specific user + category and only
// for 90 days from issue.
//
// Phase C of the public-beta launch plan.
func UnsubscribeHandler(signer *UnsubscribeSigner, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			renderUnsubResult(w, "Missing link", "The unsubscribe link is incomplete. Copy the link from the mail again, or open the console and manage your mailing preferences there.", http.StatusBadRequest)
			return
		}
		userID, category, err := signer.Verify(token)
		if err != nil {
			switch {
			case errors.Is(err, ErrExpiredUnsubscribeToken):
				renderUnsubResult(w, "Link expired",
					"This unsubscribe link expired. Open the console and unsubscribe from your account settings instead, or reply to any Eurobase mail asking to be removed.",
					http.StatusGone)
			default:
				renderUnsubResult(w, "Invalid link",
					"The unsubscribe link is invalid. Copy the link from the mail again, or reply to any Eurobase mail asking to be removed.",
					http.StatusBadRequest)
			}
			return
		}

		if _, err := pool.Exec(r.Context(),
			`INSERT INTO mailing_preferences (user_id, category, opted_out_at, updated_at)
			 VALUES ($1, $2, now(), now())
			 ON CONFLICT (user_id, category) DO UPDATE
			    SET opted_out_at = now(), updated_at = now()`,
			userID, category,
		); err != nil {
			slog.Error("unsubscribe write failed", "user_id", userID, "category", category, "error", err)
			renderUnsubResult(w, "Something went wrong",
				"We couldn't update your preference just now. Try again in a minute, or reply to any Eurobase mail asking to be removed.",
				http.StatusInternalServerError)
			return
		}
		slog.Info("unsubscribed", "user_id", userID, "category", category)

		renderUnsubResult(w, "You're unsubscribed",
			"You won't receive further "+humanCategory(category)+" mail from Eurobase. Transactional mail from your own project (verification, password reset, magic link) is separate and still works.",
			http.StatusOK)
	}
}

// humanCategory turns the DB enum into user-facing wording.
func humanCategory(category string) string {
	switch category {
	case "onboarding":
		return "onboarding"
	case "beta_updates":
		return "beta-update"
	case "usage_alerts":
		return "usage-alert"
	case "all":
		return "platform"
	default:
		return category
	}
}

// unsubResultTemplate is the one-page confirmation HTML shown after
// (or in place of) an unsubscribe. Same 600 px inline-styled shape
// as the drip mails so it doesn't jar visually. No JS.
var unsubResultTemplate = template.Must(template.New("unsub").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.Heading}} — Eurobase</title>
</head>
<body style="margin:0; padding:0; background-color:#f3f4f6; font-family:-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#f3f4f6; padding:48px 0;">
    <tr>
      <td align="center">
        <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px; width:100%; background-color:#ffffff; border-radius:12px; overflow:hidden;">
          <tr>
            <td style="background-color:#1d4ed8; padding:28px 32px;">
              <p style="margin:0; font-size:22px; font-weight:700; color:#ffffff;">Eurobase</p>
            </td>
          </tr>
          <tr>
            <td style="padding:32px;">
              <h1 style="margin:0 0 12px; font-size:22px; color:#111827;">{{.Heading}}</h1>
              <p style="margin:0 0 16px; font-size:15px; line-height:1.6; color:#374151;">{{.Message}}</p>
              <p style="margin:0; font-size:14px; line-height:1.6; color:#374151;">
                Manage all your preferences in the <a href="https://console.eurobase.app/account/mail" style="color:#1d4ed8;">console → Account → Mail</a>.
              </p>
            </td>
          </tr>
          <tr>
            <td style="padding:20px 32px; border-top:1px solid #e5e7eb;">
              <p style="margin:0; font-size:12px; color:#9ca3af;">Eurobase &middot; EU-sovereign backend-as-a-service &middot; Made in Berlin, hosted in France.</p>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`))

// renderUnsubResult writes the confirmation page with the given
// heading + message. Sets the given HTTP status (200 on success,
// 400 / 410 / 500 on failure) so monitoring can distinguish.
func renderUnsubResult(w http.ResponseWriter, heading, message string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = unsubResultTemplate.Execute(w, struct{ Heading, Message string }{heading, message})
}
