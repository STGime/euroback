package tenant

// Discord contact-form notifications — real-time ping when someone
// submits the marketing-site contact widget, so a superadmin can
// respond without refreshing /admin/contact-requests.
//
// Same fire-and-forget shape as internal/auth/signup_notify.go —
// missing webhook URL = silent no-op, failed delivery logs a warning
// and is swallowed. A dropped Discord ping is acceptable; a hung
// contact-form response is not.

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

// notifyContactAsync fires a Discord webhook in a goroutine so the
// contact-form response is never blocked on Discord's availability.
// message is truncated to 500 characters in the embed so a
// well-formed request can't flood the channel.
func notifyContactAsync(name, email, message string) {
	if discordContactWebhookURL == "" {
		return
	}
	go func() {
		// Independent short-timeout ctx — parent request ctx is
		// gone once the response ships, and we shouldn't tie the
		// webhook lifetime to the request's cancellation anyway.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Truncate message for the embed preview — full body lives
		// in the DB. Keeping the Discord channel readable when
		// someone pastes 5,000 characters is worth the trade. Cut
		// on rune boundary (not byte) so a multibyte codepoint at
		// the boundary doesn't produce invalid UTF-8, which Discord
		// rejects with 400.
		const previewMax = 500
		preview := message
		if utf8.RuneCountInString(preview) > previewMax {
			preview = string([]rune(preview)[:previewMax]) + "…"
		}

		displayName := name
		if displayName == "" {
			displayName = "(no name)"
		}

		payload := map[string]any{
			"embeds": []map[string]any{{
				"title":       "✉️ New contact-form message",
				"description": fmt.Sprintf("**From:** %s <`%s`>\n\n%s", displayName, email, preview),
				"url":         "https://console.eurobase.app/admin",
				"color":       0x3b82f6, // blue-500
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
			}},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			slog.Warn("Discord contact notify: marshal failed", "error", err)
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordContactWebhookURL, bytes.NewReader(body))
		if err != nil {
			slog.Warn("Discord contact notify: build request failed", "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Warn("Discord contact notify: request failed", "error", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Warn("Discord contact notify: non-2xx response", "status", resp.StatusCode)
		}
	}()
}
