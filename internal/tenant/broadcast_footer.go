package tenant

import (
	"html"
	"os"
	"strings"
)

// Every product update carries this footer: how to stop receiving
// them. Broadcasts go out as one BCC message, so there's no personal
// unsubscribe link; accounts switch them off under Account → Mail,
// invite-list addresses without an account reply to the email.

func broadcastConsoleURL() string {
	if u := strings.TrimRight(os.Getenv("CONSOLE_URL"), "/"); u != "" {
		return u
	}
	return "https://console.eurobase.app"
}

// withBroadcastFooter adds the opt-out note before </body>, or at the
// end when the body isn't a full HTML document.
func withBroadcastFooter(body, consoleURL string) string {
	link := html.EscapeString(consoleURL + "/account#mail")
	footer := `<p style="margin:16px auto 24px; max-width:600px; padding:0 16px; font-family:-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; font-size:12px; line-height:1.6; color:#9ca3af; text-align:center;">` +
		`Don't want product updates? Turn them off in the console under <a href="` + link + `" style="color:#6b7280; text-decoration:underline;">Account &rarr; Mail</a>, or reply to this email.</p>`
	if i := strings.LastIndex(strings.ToLower(body), "</body>"); i >= 0 {
		return body[:i] + footer + "\n" + body[i:]
	}
	return body + "\n" + footer
}
