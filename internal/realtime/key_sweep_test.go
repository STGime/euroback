package realtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestSweepRevokedKeys: a connection whose API key no longer exists is
// closed; one whose key is live, and a JWT connection, stay open; a failed
// lookup closes nothing.
func TestSweepRevokedKeys(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	// The token is the key hash here; "jwt" connects without one.
	srv := httptest.NewServer(HandleWebSocket(hub, func(_ context.Context, token, _ string) (AuthorizedClient, error) {
		ac := AuthorizedClient{ProjectID: "p1", Plan: "free", Service: true}
		if token != "jwt" {
			ac.APIKeyHash = token
		}
		return ac, nil
	}, func(*http.Request) bool { return true }, false))
	defer srv.Close()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)

	open := func(token string) *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial(wsURL+"?token="+token, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", token, err)
		}
		t.Cleanup(func() { c.Close() })
		_, _, _ = c.ReadMessage() // welcome
		return c
	}
	oldA, oldB, live, jwt := open("old"), open("old"), open("live"), open("jwt")

	// Wait for the hub to register all four.
	deadline := time.Now().Add(2 * time.Second)
	for hub.ConnectionCount("p1") < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("connections registered: %d", hub.ConnectionCount("p1"))
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := hub.SweepRevokedKeys(context.Background(), func(context.Context, []string) (map[string]bool, error) {
		return nil, errors.New("db down")
	}); n != 0 {
		t.Fatalf("failed lookup closed %d connections", n)
	}

	var mu sync.Mutex
	var asked []string
	n := hub.SweepRevokedKeys(context.Background(), func(_ context.Context, hashes []string) (map[string]bool, error) {
		mu.Lock()
		asked = hashes
		mu.Unlock()
		return map[string]bool{"live": true}, nil
	})
	if n != 2 {
		t.Fatalf("closed %d connections, want 2", n)
	}
	if len(asked) != 2 {
		t.Fatalf("looked up %v, want each key once", asked)
	}

	closed := func(c *websocket.Conn) bool {
		_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, _, err := c.ReadMessage()
		var ne interface{ Timeout() bool }
		return err != nil && !(errors.As(err, &ne) && ne.Timeout())
	}
	for name, c := range map[string]*websocket.Conn{"old A": oldA, "old B": oldB} {
		if !closed(c) {
			t.Errorf("%s: still open after its key was revoked", name)
		}
	}
	for name, c := range map[string]*websocket.Conn{"live": live, "jwt": jwt} {
		if closed(c) {
			t.Errorf("%s: closed, want open", name)
		}
	}
}
