package tenant

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a bytes.Buffer safe to write from the notifier's
// goroutine while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureWebhook starts a server that hands each request body to a channel.
func captureWebhook(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()
	bodies := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, bodies
}

func waitBody(t *testing.T, bodies chan string) string {
	t.Helper()
	select {
	case b := <-bodies:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("no webhook request")
		return ""
	}
}

// Discord is a US service: the notices say only that something arrived and
// link to the inbox. The notifiers take no content at all; this pins what
// they send.
func TestDiscordNoticesCarryNoContent(t *testing.T) {
	srv, bodies := captureWebhook(t)
	oldSupport, oldContact := discordSupportWebhookURL, discordContactWebhookURL
	t.Cleanup(func() { discordSupportWebhookURL, discordContactWebhookURL = oldSupport, oldContact })
	discordSupportWebhookURL, discordContactWebhookURL = srv.URL, srv.URL

	notifySupportAsync("billing", "urgent")
	b := waitBody(t, bodies)
	for _, want := range []string{"/admin/inbox", "billing", "urgent", "New Team-tier support ticket"} {
		if !strings.Contains(b, want) {
			t.Errorf("support notice lacks %q: %s", want, b)
		}
	}

	notifyContactAsync()
	notifyTeamBetaRequestAsync()
	for i := 0; i < 2; i++ {
		b := waitBody(t, bodies)
		if !strings.Contains(b, "/admin/inbox") {
			t.Errorf("notice lacks the inbox link: %s", b)
		}
		if strings.Contains(b, "@") {
			t.Errorf("notice contains an @ (an e-mail address?): %s", b)
		}
	}
}

// A failed request must not log the webhook URL (it holds the token).
func TestDiscordNoticeErrorLogsNoURL(t *testing.T) {
	var buf lockedBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	const secret = "SECRET-WEBHOOK-TOKEN"
	// Nothing listens on port 1: the request fails at once.
	postDiscordAsync("http://127.0.0.1:1/api/webhooks/123/"+secret, "test", map[string]any{"title": "x"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(buf.String(), "request failed") {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, "request failed") {
		t.Fatalf("expected a logged failure, got %q", out)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "/api/webhooks/") {
		t.Errorf("the log contains the webhook URL: %s", out)
	}
}

func TestWebhookErrDropsURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://discord.example/api/webhooks/1/TOKEN", nil)
	_, err := http.DefaultClient.Do(req)
	if err == nil || !strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("expected the raw error to name the URL, got %v", err)
	}
	if got := webhookErr(err); strings.Contains(got.Error(), "TOKEN") {
		t.Errorf("webhookErr kept the URL: %v", got)
	}
	plain := errors.New("plain")
	if webhookErr(plain) != plain {
		t.Error("other errors pass through unchanged")
	}
}
