package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func timeoutSoon() time.Time { return time.Now().Add(200 * time.Millisecond) }

// Closes #62. Exercise the new project_id + Authorize contract on the
// WebSocket handler. Focused on behaviour the realtime package owns;
// the real-DB platform/end-user JWT dispatch in router.go is covered
// by integration-style tests there.

func startServerWithAuthorize(t *testing.T, authorize Authorize, devMode bool) (string, func()) {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	srv := httptest.NewServer(HandleWebSocket(hub, authorize, nil, devMode))
	return strings.Replace(srv.URL, "http://", "ws://", 1), srv.Close
}

func dial(t *testing.T, wsURL, query string) (*http.Response, error) {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial(wsURL+"?"+query, http.Header{"Origin": []string{"http://test"}})
	if c != nil {
		_ = c.Close()
	}
	return resp, err
}

func TestRealtime_NonApikeyTokenWithoutProjectIDFails(t *testing.T) {
	// The realtime package delegates the "is project_id required for
	// this token shape" decision to the Authorize callback. A typical
	// production implementation accepts apikey tokens without a
	// project_id but requires it for JWTs. Modelled here as: empty
	// input → ErrUnauthorized.
	wsURL, stop := startServerWithAuthorize(t, func(_ context.Context, _, qpID string) (string, string, error) {
		if qpID == "" {
			return "", "", ErrUnauthorized
		}
		return qpID, "free", nil
	}, false)
	defer stop()

	resp, err := dial(t, wsURL, "token=t-not-an-apikey")
	if err == nil {
		t.Fatal("expected handshake to fail when project_id is missing for a JWT-shaped token")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %v", resp)
	}
}

func TestRealtime_ApikeyResolvedProjectID(t *testing.T) {
	// Apikey path: callback resolves project_id from the token alone;
	// project_id query param can be omitted. The hub keys connections
	// on the resolved project.
	hub := NewHub()
	go hub.Run()
	originOK := func(_ *http.Request) bool { return true }
	authorize := func(_ context.Context, token, _ string) (string, string, error) {
		if token == "eb_pk_abc" {
			return "p-from-apikey", "pro", nil
		}
		return "", "", ErrUnauthorized
	}
	srv := httptest.NewServer(HandleWebSocket(hub, authorize, originOK, false))
	defer srv.Close()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)

	c, _, err := websocket.DefaultDialer.Dial(wsURL+"?token=eb_pk_abc",
		http.Header{"Origin": []string{"http://test"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// The welcome frame names the resolved project — proves the
	// callback's returned projectID is what the hub keys on.
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	if !strings.Contains(string(msg), "p-from-apikey") {
		t.Errorf("expected welcome to name p-from-apikey, got %q", string(msg))
	}
}

func TestRealtime_RejectsMissingToken(t *testing.T) {
	wsURL, stop := startServerWithAuthorize(t, func(_ context.Context, _, qpID string) (string, string, error) {
		return qpID, "free", nil
	}, false)
	defer stop()

	resp, err := dial(t, wsURL, "project_id=p-1")
	if err == nil {
		t.Fatal("expected handshake to fail with no token")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %v", resp)
	}
}

func TestRealtime_AuthorizeReturnsUnauthorized(t *testing.T) {
	wsURL, stop := startServerWithAuthorize(t, func(_ context.Context, _, qpID string) (string, string, error) {
		return "", "", ErrUnauthorized
	}, false)
	defer stop()

	resp, err := dial(t, wsURL, "token=bad&project_id=p-1")
	if err == nil {
		t.Fatal("expected handshake to fail with bad token")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %v", resp)
	}
}

func TestRealtime_AuthorizeReturnsForbidden(t *testing.T) {
	wsURL, stop := startServerWithAuthorize(t, func(_ context.Context, _, qpID string) (string, string, error) {
		return "", "", ErrForbidden
	}, false)
	defer stop()

	resp, err := dial(t, wsURL, "token=good&project_id=p-other")
	if err == nil {
		t.Fatal("expected handshake to fail when user is not a project member")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %v", resp)
	}
}

func TestRealtime_AuthorizeReceivesQueriedProjectID(t *testing.T) {
	var captured atomic.Value
	wsURL, stop := startServerWithAuthorize(t, func(_ context.Context, token, projectID string) (string, string, error) {
		captured.Store([2]string{token, projectID})
		return projectID, "pro", nil
	}, false)
	defer stop()

	// We don't care about the upgrade outcome here; just observe what
	// the callback received.
	_, _ = dial(t, wsURL, "token=tok-abc&project_id=p-xyz")

	v, ok := captured.Load().([2]string)
	if !ok {
		t.Fatal("authorize callback was not invoked")
	}
	if v[0] != "tok-abc" || v[1] != "p-xyz" {
		t.Errorf("authorize got (%q, %q), want (tok-abc, p-xyz)", v[0], v[1])
	}
}

func TestRealtime_PublishWithProjectID_DeliversToMatchingSubscriber(t *testing.T) {
	// End-to-end intent of #62: publisher emits with projectID X, a
	// subscriber connected as project_id=X receives the event.
	hub := NewHub()
	go hub.Run()
	authorize := func(_ context.Context, _, qpID string) (string, string, error) { return qpID, "pro", nil }
	originOK := func(_ *http.Request) bool { return true }
	srv := httptest.NewServer(HandleWebSocket(hub, authorize, originOK, false))
	defer srv.Close()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)

	c, _, err := websocket.DefaultDialer.Dial(wsURL+"?token=t&project_id=p-match",
		http.Header{"Origin": []string{"http://test"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Drain the welcome frame, subscribe to the channel, drain the
	// "subscribed" ack — only after these does the hub key route
	// future broadcasts to this client.
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	if err := c.WriteJSON(map[string]string{"action": "subscribe", "channel": "db:todos"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("read subscribed ack: %v", err)
	}

	pub := NewEventPublisher(nil, hub)
	if err := pub.PublishInsert(context.Background(), "p-match", "todos", map[string]interface{}{"id": "1"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	c.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if !strings.Contains(string(msg), `"INSERT"`) {
		t.Errorf("expected INSERT event, got %q", string(msg))
	}
}

func TestRealtime_PublishWithDifferentProjectID_DoesNotDeliver(t *testing.T) {
	// Negative side of the same property: publish to project Y is NOT
	// delivered to a subscriber on project X. The pre-#62 bug was that
	// every event got dropped because schema and project_id never
	// matched; this test guards against a regression in the other
	// direction (over-broadcast).
	hub := NewHub()
	go hub.Run()
	authorize := func(_ context.Context, _, qpID string) (string, string, error) { return qpID, "pro", nil }
	originOK := func(_ *http.Request) bool { return true }
	srv := httptest.NewServer(HandleWebSocket(hub, authorize, originOK, false))
	defer srv.Close()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)

	c, _, err := websocket.DefaultDialer.Dial(wsURL+"?token=t&project_id=p-x",
		http.Header{"Origin": []string{"http://test"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	if err := c.WriteJSON(map[string]string{"action": "subscribe", "channel": "db:todos"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("read subscribed ack: %v", err)
	}

	pub := NewEventPublisher(nil, hub)
	_ = pub.PublishInsert(context.Background(), "p-y", "todos", map[string]interface{}{"id": "1"})

	// Set a short deadline; expecting timeout (no event reached us
	// because our project_id is p-x, not p-y).
	c.SetReadDeadline(timeoutSoon())
	_, _, err = c.ReadMessage()
	if err == nil {
		t.Fatal("subscriber received an event from a different project — cross-project leak")
	}
}
