package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The sign-up notice names the new account's e-mail address and the running
// total; a backtick in the address can't break out of the code span.
func TestSignupNoticeNamesEmail(t *testing.T) {
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

	notifySignupAsync("new`user@example.org", 42)
	select {
	case b := <-bodies:
		if !strings.Contains(b, "42") || !strings.Contains(b, "`newuser@example.org`") {
			t.Errorf("unexpected sign-up notice: %s", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no webhook request")
	}
}

func TestNoticeEmailCleansInput(t *testing.T) {
	if got := noticeEmail("a`b\nc\r\td@example.org"); got != "abcd@example.org" {
		t.Errorf("control characters / backticks kept: %q", got)
	}
	long := strings.Repeat("x", 5000) + "@example.org"
	if got := []rune(noticeEmail(long)); len(got) != 255 {
		t.Errorf("long address not cut: %d runes", len(got))
	}
}
