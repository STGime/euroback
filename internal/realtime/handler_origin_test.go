package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// Closes #47. The WS upgrader used to call CheckOrigin → return true
// unconditionally, leaving the realtime endpoint open to cross-site
// WebSocket hijacking from any origin. These tests assert the new
// origin checker is plumbed through and that the previous fail-open
// behaviour is gone.

// startServer spins up an httptest server in front of HandleWebSocket
// with the given origin checker and dev-mode flag. Returns the WS URL
// and a cleanup. In production mode the handler is wired with stub
// auth (any token accepted, fixed tenant returned) so we can exercise
// the CheckOrigin path past the auth gate.
func startServer(t *testing.T, originChecker func(*http.Request) bool, devMode bool) (string, func()) {
	t.Helper()
	hub := NewHub()
	go hub.Run()

	var authorize Authorize
	if !devMode {
		// Stub authorize: accept any token, return "free" plan so we
		// can exercise the CheckOrigin path past the auth gate. Real
		// auth is exercised in router/realtime integration tests.
		authorize = func(_ context.Context, _, _ string) (string, error) {
			return "free", nil
		}
	}

	srv := httptest.NewServer(HandleWebSocket(hub, authorize, originChecker, devMode))
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)
	return wsURL, srv.Close
}

func dialWith(t *testing.T, wsURL, origin string) (*http.Response, error) {
	t.Helper()
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	// Both project_id and token are needed in production mode; dev mode
	// fills in a default project_id so the token can be omitted.
	c, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token=t&project_id=p-1", hdr)
	if c != nil {
		_ = c.Close()
	}
	return resp, err
}

func TestCheckOrigin_ProductionRejectsDisallowedOrigin(t *testing.T) {
	checker := func(r *http.Request) bool {
		return r.Header.Get("Origin") == "https://app.example.com"
	}
	wsURL, stop := startServer(t, checker, false)
	defer stop()

	resp, err := dialWith(t, wsURL, "https://attacker.example")
	if err == nil {
		t.Fatal("expected handshake to fail for disallowed origin")
	}
	// gorilla/websocket surfaces 403 when CheckOrigin returns false.
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		dump, _ := httputil.DumpResponse(resp, false)
		t.Errorf("expected 403, got %v\n%s", resp, string(dump))
	}
}

func TestCheckOrigin_ProductionAcceptsAllowedOrigin(t *testing.T) {
	checker := func(r *http.Request) bool {
		return r.Header.Get("Origin") == "https://app.example.com"
	}
	wsURL, stop := startServer(t, checker, false)
	defer stop()

	resp, err := dialWith(t, wsURL, "https://app.example.com")
	if err != nil {
		body := ""
		if resp != nil {
			d, _ := httputil.DumpResponse(resp, false)
			body = string(d)
		}
		t.Fatalf("expected handshake to succeed for allowed origin, got %v\n%s", err, body)
	}
}

func TestCheckOrigin_ProductionRejectsMissingOrigin(t *testing.T) {
	// Browsers always send Origin on cross-origin requests. A missing
	// header in production is suspicious; we fail closed (the checker
	// returns false on empty Origin).
	checker := func(r *http.Request) bool {
		return r.Header.Get("Origin") != ""
	}
	wsURL, stop := startServer(t, checker, false)
	defer stop()

	resp, err := dialWith(t, wsURL, "")
	if err == nil {
		t.Fatal("expected handshake to fail when Origin missing in production")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %v", resp)
	}
}

func TestCheckOrigin_ProductionWithNilCheckerFailsClosed(t *testing.T) {
	// A misconfigured production deploy (originChecker = nil) used to
	// fall through to "allow all". The new code rejects.
	wsURL, stop := startServer(t, nil, false)
	defer stop()

	resp, err := dialWith(t, wsURL, "https://app.example.com")
	if err == nil {
		t.Fatal("expected handshake to fail when checker is nil in production")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 fail-closed, got %v", resp)
	}
}

func TestCheckOrigin_DevModeAcceptsAnyOrigin(t *testing.T) {
	wsURL, stop := startServer(t, nil, true)
	defer stop()

	resp, err := dialWith(t, wsURL, "https://anything.example")
	if err != nil {
		body := ""
		if resp != nil {
			d, _ := httputil.DumpResponse(resp, false)
			body = string(d)
		}
		t.Fatalf("expected dev mode to accept any origin, got %v\n%s", err, body)
	}
}

// Sanity: the URL helper isn't doing anything funny.
func TestStartServer_URLShape(t *testing.T) {
	wsURL, stop := startServer(t, nil, true)
	defer stop()
	if u, err := url.Parse(wsURL); err != nil || u.Scheme != "ws" {
		t.Errorf("expected ws://… URL, got %q (%v)", wsURL, err)
	}
}
