package clientip

import (
	"net/http/httptest"
	"testing"
)

func TestFromRequest_TrustedHops(t *testing.T) {
	cases := []struct {
		name   string
		xff    string
		remote string
		trust  bool
		hops   int
		want   string
	}{
		// The reported bypass: spoofed leftmost entry must be ignored.
		{"spoofed leftmost ignored, real client is rightmost (1 hop)", "8.8.4.4, 203.0.113.7", "10.0.0.5:4444", true, 1, "203.0.113.7"},
		{"single entry appended by proxy", "203.0.113.7", "10.0.0.5:4444", true, 1, "203.0.113.7"},
		{"two trusted hops (LB + nginx)", "8.8.4.4, 203.0.113.7, 10.1.1.1", "10.0.0.5:4444", true, 2, "203.0.113.7"},
		{"chain shorter than hops fails closed to peer", "203.0.113.7", "10.0.0.5:4444", true, 2, "10.0.0.5"},
		{"no XFF falls back to peer", "", "198.51.100.9:1234", true, 1, "198.51.100.9"},
		{"trustProxy=false ignores XFF entirely", "8.8.4.4", "198.51.100.9:1234", false, 1, "198.51.100.9"},
		{"hops<1 treated as 1", "8.8.4.4, 203.0.113.7", "10.0.0.5:4444", true, 0, "203.0.113.7"},
		{"empty entries dropped", "8.8.4.4,, 203.0.113.7 ,", "10.0.0.5:4444", true, 1, "203.0.113.7"},
		{"ipv6 peer without port brackets", "", "[::1]:5555", true, 1, "::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := FromRequest(r, tc.trust, tc.hops); got != tc.want {
				t.Errorf("FromRequest(xff=%q remote=%q trust=%v hops=%d) = %q, want %q", tc.xff, tc.remote, tc.trust, tc.hops, got, tc.want)
			}
		})
	}
}
