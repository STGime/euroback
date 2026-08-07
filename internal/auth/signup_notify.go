package auth

// Discord signup notifications — real-time "someone new signed up"
// pings to the founder, so the public-beta launch doesn't require
// refreshing /admin/signup-users every 5 minutes.
//
// Same shape as the existing DISCORD_ALERTS_WEBHOOK plumbing in
// deploy/k8s/cert-monitor-cronjob.yaml + health-monitor-cronjob.yaml.
// Optional — if DISCORD_SIGNUPS_WEBHOOK is unset the notifier is a
// silent no-op, matching the codebase convention of graceful-degrade
// on missing observability config.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// discordSignupWebhookURL is set by cmd/gateway/main.go from the
// eurobase-secrets Secret. Blank → notifier is a no-op.
var discordSignupWebhookURL string

// SetDiscordSignupsWebhook wires the webhook URL. Called once at
// gateway startup; empty is legal (dev, or a decision not to notify).
func SetDiscordSignupsWebhook(url string) {
	discordSignupWebhookURL = url
	if url != "" {
		slog.Info("Discord signup notifications enabled")
	}
}

// notifySignupAsync fires a Discord webhook in a goroutine so the
// signup response is never blocked on Discord's availability.
// A failed webhook logs a warning and is swallowed — a missed
// signup ping is acceptable, a hung signup response isn't.
//
// totalSignups is a cheap COUNT(*) the caller passes in — computed
// once inside the caller's tx so we avoid a second DB roundtrip on
// the hot signup path.
func notifySignupAsync(email string, totalSignups int64) {
	if discordSignupWebhookURL == "" {
		return
	}
	go func() {
		// Independent short-timeout ctx — parent request ctx is
		// gone once the response ships, and we shouldn't tie the
		// webhook lifetime to the request's cancellation anyway.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		payload := map[string]any{
			"embeds": []map[string]any{{
				"title":       "🎉 New signup",
				"description": fmt.Sprintf("`%s` just signed up.\n\n**Total signups:** %d", email, totalSignups),
				"url":         "https://console.eurobase.app/admin",
				"color":       0x10b981, // emerald-500
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
			}},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			slog.Warn("Discord signup notify: marshal failed", "error", err)
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordSignupWebhookURL, bytes.NewReader(body))
		if err != nil {
			slog.Warn("Discord signup notify: build request failed", "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Warn("Discord signup notify: request failed", "error", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Warn("Discord signup notify: non-2xx response", "status", resp.StatusCode)
		}
	}()
}
