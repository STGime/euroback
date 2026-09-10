package tenant

// Discord support-ticket notifications — real-time ping when a
// Team-tier user files a priority-support ticket. Same fire-and-forget
// shape as contact_notify.go — missing webhook URL = silent no-op,
// failed delivery logs a warning and is swallowed. A dropped Discord
// ping is acceptable; a hung support-form response is not.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"
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

// notifySupportAsync fires a Discord webhook in a goroutine so the
// support-form response is never blocked on Discord's availability.
// Preview is capped at 500 characters so a pasted stack trace doesn't
// flood the channel — the full body lives in support_requests.
func notifySupportAsync(email, subject, message, category, priority string, orgID, projectID *string) {
	if discordSupportWebhookURL == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		const previewMax = 500
		preview := message
		if utf8.RuneCountInString(preview) > previewMax {
			preview = string([]rune(preview)[:previewMax]) + "…"
		}

		// Priority-coded colour + emoji so ops can eyeball urgent
		// vs normal tickets at a glance in Discord.
		//   normal → blue-500
		//   urgent → amber-500
		colour := 0x3b82f6
		badge := "🎫"
		if priority == "urgent" {
			colour = 0xf59e0b
			badge = "🚨"
		}

		// Optional context suffix — omit when both nil to keep the
		// common case clean.
		contextLine := ""
		if orgID != nil && *orgID != "" {
			contextLine += fmt.Sprintf("\n**Org:** `%s`", *orgID)
		}
		if projectID != nil && *projectID != "" {
			contextLine += fmt.Sprintf("\n**Project:** `%s`", *projectID)
		}

		payload := map[string]any{
			"embeds": []map[string]any{{
				"title": fmt.Sprintf("%s Team-tier support: %s", badge, subject),
				"description": fmt.Sprintf(
					"**From:** `%s`\n**Category:** %s · **Priority:** %s%s\n\n%s",
					email, category, priority, contextLine, preview,
				),
				"url":       "https://console.eurobase.app/admin",
				"color":     colour,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			}},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			slog.Warn("Discord support notify: marshal failed", "error", err)
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordSupportWebhookURL, bytes.NewReader(body))
		if err != nil {
			slog.Warn("Discord support notify: build request failed", "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Warn("Discord support notify: request failed", "error", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Warn("Discord support notify: non-2xx response", "status", resp.StatusCode)
		}
	}()
}
