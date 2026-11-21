package tenant

// Discord contact-form notifications — real-time ping when someone
// submits the marketing-site contact widget or asks for Team-beta access.
// The ping carries no name, e-mail or message (Discord is a US service);
// the message is read in Admin → Inbox.
//
// Same fire-and-forget shape as internal/auth/signup_notify.go —
// missing webhook URL = silent no-op, failed delivery logs a warning
// and is swallowed. A dropped Discord ping is acceptable; a hung
// contact-form response is not.

import (
	"log/slog"
)

// discordContactWebhookURL is set by cmd/gateway/main.go from the
// eurobase-secrets Secret. Blank → notifier is a no-op.
var discordContactWebhookURL string

// SetDiscordContactWebhook wires the webhook URL. Called once at
// gateway startup; empty is legal (dev, or a decision not to notify).
func SetDiscordContactWebhook(url string) {
	discordContactWebhookURL = url
	if url != "" {
		slog.Info("Discord contact notifications enabled")
	}
}

// notifyContactAsync pings Discord that a contact-form message arrived.
func notifyContactAsync() {
	if discordContactWebhookURL == "" {
		return
	}
	postDiscordAsync(discordContactWebhookURL, "contact", map[string]any{
		"title":       "✉️ New contact-form message",
		"description": "[Read it in Admin → Inbox](" + inboxURL + ")",
		"url":         inboxURL,
		"color":       0x3b82f6, // blue-500
	})
}

// notifyTeamBetaRequestAsync pings Discord that someone asked for Team
// beta access (same webhook, its own title and colour).
func notifyTeamBetaRequestAsync() {
	if discordContactWebhookURL == "" {
		return
	}
	postDiscordAsync(discordContactWebhookURL, "team beta request", map[string]any{
		"title":       "🎯 Team-tier beta access request",
		"description": "[Read it in Admin → Inbox](" + inboxURL + ")",
		"url":         inboxURL,
		"color":       0x10b981, // emerald-500, distinct from the contact-form blue
	})
}
