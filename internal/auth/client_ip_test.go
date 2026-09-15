package auth

import (
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/clientip"
)

// Pins the fix for the XFF rate-limit bypass / audit-IP poisoning: the
// platform routes must resolve the client from the trusted-proxy side
// of X-Forwarded-For, never the client-controlled leftmost entry.
func TestClientIP_IgnoresSpoofedLeftmostXFF(t *testing.T) {
	origTrust, origHops := clientip.TrustProxy, clientip.TrustedHops
	t.Cleanup(func() { clientip.TrustProxy, clientip.TrustedHops = origTrust, origHops })
	clientip.TrustProxy, clientip.TrustedHops = true, 1

	r := httptest.NewRequest("POST", "/platform/auth/signup", nil)
	r.RemoteAddr = "10.0.0.5:4444"

	// The reported bypass: attacker prepends a fake entry; the ingress
	// appended the real peer on the right.
	r.Header.Set("X-Forwarded-For", "8.8.4.4, 203.0.113.7")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Fatalf("clientIP = %q, want proxy-appended 203.0.113.7 (spoofed leftmost 8.8.4.4 must be ignored)", got)
	}

	// Two trusted hops: second-from-right is the client; a too-short
	// chain fails closed to the TCP peer instead of trusting the client.
	clientip.TrustedHops = 2
	r.Header.Set("X-Forwarded-For", "8.8.4.4, 203.0.113.7, 10.1.1.1")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Fatalf("2-hop = %q, want 203.0.113.7", got)
	}
	r.Header.Set("X-Forwarded-For", "8.8.4.4")
	if got := clientIP(r); got != "10.0.0.5" {
		t.Fatalf("short chain should fail closed to peer 10.0.0.5, got %q", got)
	}

	// No XFF → TCP peer.
	r.Header.Del("X-Forwarded-For")
	if got := clientIP(r); got != "10.0.0.5" {
		t.Fatalf("no XFF = %q, want peer 10.0.0.5", got)
	}
}
