package tenant

// Discord support-ticket notifications — real-time ping when a
// Team-tier user files a priority-support ticket. The ping carries no
// personal data and no message content (Discord is a US service): only
// that a ticket arrived, its category and priority, and a link to
// Admin → Inbox, where the ticket is read. Same fire-and-forget
// shape as contact_notify.go — missing webhook URL = silent no-op,
// failed delivery logs a warning and is swallowed. A dropped Discord
// ping is acceptable; a hung support-form response is not.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// discordSupportWebhookURL is set by cmd/gateway/main.go from the
// eurobase-secrets Secret. Blank → notifier is a no-op.
var discordSupportWebhookURL string

// SetDiscordSupportWebhook wires the webhook URL. Called once at
// gateway startup; empty is legal (dev, or a decision not to notify).
func SetDiscordSupportWebhook(url string) {
	discordSupportWebhookURL = url
	if url != "" {
		slog.Info("Discord support notifications enabled")
	}
}

// inboxURL is where the full messages are read (superadmins only).
const inboxURL = "https://console.eurobase.app/admin/inbox"

// notifySupportAsync pings Discord that a ticket arrived — category and
// priority only (both from fixed lists), no sender, subject or text.
func notifySupportAsync(category, priority string) {
	if discordSupportWebhookURL == "" {
		return
	}
	colour, badge := 0x3b82f6, "🎫" // normal: blue
	if priority == "urgent" {
		colour, badge = 0xf59e0b, "🚨" // urgent: amber
	}
	postDiscordAsync(discordSupportWebhookURL, "support", map[string]any{
		"title":       fmt.Sprintf("%s New Team-tier support ticket", badge),
		"description": fmt.Sprintf("**Category:** %s · **Priority:** %s\n\n[Read it in Admin → Inbox](%s)", category, priority, inboxURL),
		"url":         inboxURL,
		"color":       colour,
	})
}

// postDiscordAsync posts one embed in a goroutine, so a form response is
// never blocked on Discord; a failure is logged and dropped.
func postDiscordAsync(webhookURL, what string, embed map[string]any) {
	embed["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		body, err := json.Marshal(map[string]any{"embeds": []map[string]any{embed}})
		if err != nil {
			slog.Warn("Discord notify: marshal failed", "what", what, "error", err)
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
		if err != nil {
			slog.Warn("Discord notify: build request failed", "what", what) // the error would contain the URL
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Warn("Discord notify: request failed", "what", what, "error", webhookErr(err))
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Warn("Discord notify: non-2xx response", "what", what, "status", resp.StatusCode)
		}
	}()
}

// webhookErr drops the request URL from an HTTP client error: Go formats
// it as `Post "<url>": <cause>`, and a Discord webhook URL contains its
// secret token. Only the cause is logged.
func webhookErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
