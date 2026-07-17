package email

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UnsubscribeHandler handles the opt-out endpoint that lives in
// every outbound platform-mail footer.
//
// URL shape:
//
//	GET  /platform/mailing/unsubscribe?token=<opaque>  → confirm form
//	POST /platform/mailing/unsubscribe (token in body) → performs opt-out
//
// **Why GET is safe and POST does the write.** Corporate anti-phishing
// scanners (Microsoft Defender SafeLinks, Mimecast URL Protect,
// Proofpoint URL Defense, Barracuda, Cisco IronPort) auto-fetch every
// URL in inbound mail to detonate for phishing detection. That fetch
// would land on this endpoint carrying the real signed token, so a
// mutating GET would silently opt out every recipient behind such a
// scanner before they ever open the mail. RFC 7231 §4.2.1 requires
// GET be safe (no observable side effect); the POST/confirm shape is
// what List-Unsubscribe-Post (RFC 8058) callers expect too.
//
// Verification via UnsubscribeSigner (HMAC + expiry) is done on both
// paths — the token IS the authorisation.
//
// Phase C of the public-beta launch plan.
func UnsubscribeHandler(signer *UnsubscribeSigner, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var token string
		switch r.Method {
		case http.MethodGet:
			token = r.URL.Query().Get("token")
		case http.MethodPost:
			// Token can come from form body (browser-submitted confirm
			// form) or from the URL for List-Unsubscribe-Post callers
			// (RFC 8058) that POST with an empty body.
			_ = r.ParseForm()
			token = r.PostFormValue("token")
			if token == "" {
				token = r.URL.Query().Get("token")
			}
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

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

		if r.Method == http.MethodGet {
			renderUnsubConfirm(w, token, humanCategory(category))
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

// unsubConfirmTemplate is the page GET renders. Same 600 px shape as
// the confirmation page + drip mails. Body is a POST form the user
// must click — mail scanners don't submit POSTs, so this closes the
// silent-opt-out hazard (bug #002).
var unsubConfirmTemplate = template.Must(template.New("unsubConfirm").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Confirm unsubscribe — Eurobase</title>
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
              <h1 style="margin:0 0 12px; font-size:22px; color:#111827;">Unsubscribe from {{.Category}} mail?</h1>
              <p style="margin:0 0 16px; font-size:15px; line-height:1.6; color:#374151;">
                Click the button below to stop receiving further {{.Category}} mail from Eurobase. Transactional mail sent from your own project (verification, password reset, magic link) is separate and will keep working.
              </p>
              <form method="post" action="/platform/mailing/unsubscribe" style="margin:0;">
                <input type="hidden" name="token" value="{{.Token}}">
                <button type="submit" style="background-color:#1d4ed8; color:#ffffff; border:0; border-radius:8px; padding:12px 22px; font-size:15px; font-weight:600; cursor:pointer;">
                  Unsubscribe
                </button>
              </form>
              <p style="margin:16px 0 0; font-size:13px; color:#6b7280;">
                Or manage all preferences in the <a href="https://console.eurobase.app/account#mail" style="color:#1d4ed8;">console → Account → Mail</a>.
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

// unsubPageCSP overrides the gateway's global JSON-API CSP
// (`form-action 'none'`) for the unsubscribe pages. Without it, the
// browser silently blocks the confirm-form POST — the button
// visually clicks but nothing happens. Only the two mailing pages
// serve real HTML forms; every other gateway response is JSON.
//
// Kept nearly as tight as the default: no external resources,
// clickjacking blocked, `form-action 'self'` scoped exactly to this
// origin so the POST to `/platform/mailing/unsubscribe` works.
// `style-src 'unsafe-inline'` allows the inline styling that keeps
// the page's visual identity aligned with the drip mails.
const unsubPageCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// renderUnsubConfirm renders the confirm-form page. Called on GET
// when the token verifies. HTTP 200 — the token was valid, the user
// (or their mail scanner) can safely look at this page without any
// state change.
func renderUnsubConfirm(w http.ResponseWriter, token, category string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", unsubPageCSP)
	// Extra belt-and-braces: tell shared proxies not to cache the
	// confirm page. Scanners that cache would otherwise cache the
	// HTML that carries the raw token as a form value.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = unsubConfirmTemplate.Execute(w, struct{ Token, Category string }{token, category})
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
                Manage all your preferences in the <a href="https://console.eurobase.app/account#mail" style="color:#1d4ed8;">console → Account → Mail</a>.
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
// 400 / 410 / 500 on failure) so monitoring can distinguish. CSP
// override matches renderUnsubConfirm so inline styles render.
func renderUnsubResult(w http.ResponseWriter, heading, message string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", unsubPageCSP)
	w.WriteHeader(status)
	_ = unsubResultTemplate.Execute(w, struct{ Heading, Message string }{heading, message})
}
