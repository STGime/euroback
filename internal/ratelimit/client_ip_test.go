package ratelimit

import (
	"net/http/httptest"
	"testing"
)

// #228: ClientIPForProject is the trust-proxy-aware sibling of ClientIP.
// Three load-bearing properties the per-project gates depend on:
//
//   1. trustProxy=true  → leftmost X-Forwarded-For wins when present.
//   2. trustProxy=false → X-Forwarded-For is IGNORED entirely (even
//      when present), and the TCP peer is returned. This is the
//      anti-XFF-forgery side of the knob.
//   3. Either way, when there's no X-Forwarded-For header, the TCP
//      peer is returned (so the helper has a meaningful answer in
//      direct-connection scenarios).

func TestClientIPForProject_TrustsXFFWhenEnabled(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.5")

	got := ClientIPForProject(r, true)
	if got != "203.0.113.7" {
		t.Errorf("trustProxy=true must return leftmost XFF, got %q", got)
	}
}

func TestClientIPForProject_IgnoresXFFWhenDisabled(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.5")

	got := ClientIPForProject(r, false)
	if got != "10.0.0.5" {
		t.Errorf("trustProxy=false must use TCP peer (no XFF), got %q", got)
	}
}

func TestClientIPForProject_NoXFFFallsBackToTCPPeer(t *testing.T) {
	cases := []struct {
		name       string
		trustProxy bool
	}{
		{"trustProxy=true", true},
		{"trustProxy=false", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", nil)
			r.RemoteAddr = "192.0.2.42:9999"
			// no XFF header
			got := ClientIPForProject(r, c.trustProxy)
			if got != "192.0.2.42" {
				t.Errorf("no XFF + %s: expected TCP peer 192.0.2.42, got %q", c.name, got)
			}
		})
	}
}

func TestClientIPForProject_SingleXFFEntry(t *testing.T) {
	// nginx-ingress default writes a single XFF entry (overwrites,
	// doesn't append). The leftmost-extract path must work without a
	// comma being present.
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")

	got := ClientIPForProject(r, true)
	if got != "198.51.100.1" {
		t.Errorf("single XFF entry should be returned verbatim, got %q", got)
	}
}

// Snapshot the legacy ClientIP behaviour — confirms #228 doesn't
// regress the platform-wide helper (still used in router.go for the
// audit log's client IP and for legacy auth helpers).
func TestClientIP_LegacyBehaviorUnchanged(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.5")
	if got := ClientIP(r); got != "203.0.113.7" {
		t.Errorf("legacy ClientIP must still trust leftmost XFF, got %q", got)
	}

	r2 := httptest.NewRequest("POST", "/", nil)
	r2.RemoteAddr = "192.0.2.42:9999"
	if got := ClientIP(r2); got != "192.0.2.42" {
		t.Errorf("legacy ClientIP with no XFF must fall back to TCP peer, got %q", got)
	}
}
