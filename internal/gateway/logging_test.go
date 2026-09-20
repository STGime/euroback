package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStatusCapture_SatisfiesResponseControllerInterfaces pins the
// Hijacker / Flusher / Pusher delegation on statusCapture. Same
// class of bug as the metrics wrapper fixed 2026-09-20 — a
// ResponseWriter wrapper that hides Hijacker will 500 any WebSocket
// upgrade running under the middleware. statusCapture is currently
// applied only to /platform/projects/{id} (no WS endpoint there
// today), so this test is a guard against a future regression where
// someone lands a WS or SSE route under that prefix.
func TestStatusCapture_SatisfiesResponseControllerInterfaces(t *testing.T) {
	var w http.ResponseWriter = &statusCapture{ResponseWriter: httptest.NewRecorder(), code: http.StatusOK}
	if _, ok := w.(http.Hijacker); !ok {
		t.Error("*statusCapture must implement http.Hijacker — required for WebSocket / HTTP/1.1 upgrades")
	}
	if _, ok := w.(http.Flusher); !ok {
		t.Error("*statusCapture must implement http.Flusher — required for server-sent events / streaming responses")
	}
	if _, ok := w.(http.Pusher); !ok {
		t.Error("*statusCapture must implement http.Pusher — required for HTTP/2 server push")
	}
}
