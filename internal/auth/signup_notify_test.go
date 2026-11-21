package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The sign-up notice goes to Discord (a US service): the running total only,
// no e-mail address.
func TestSignupNoticeCarriesNoEmail(t *testing.T) {
	bodies := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	old := discordSignupWebhookURL
	t.Cleanup(func() { discordSignupWebhookURL = old })
	discordSignupWebhookURL = srv.URL

	notifySignupAsync(42)
	select {
	case b := <-bodies:
		if !strings.Contains(b, "42") || strings.Contains(b, "@") {
			t.Errorf("unexpected sign-up notice: %s", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no webhook request")
	}
}
