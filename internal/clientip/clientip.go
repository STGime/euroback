// Package clientip resolves the real client IP of an HTTP request from
// behind a known number of trusted reverse-proxy hops.
//
// It is a leaf package (net/http + strings only) so both
// internal/ratelimit and internal/auth can use one implementation —
// ratelimit imports auth, so auth cannot import ratelimit directly.
//
// Threat model: X-Forwarded-For is client-controlled on the LEFT. A
// caller can send `X-Forwarded-For: 8.8.4.4` and a naive "take the
// leftmost entry" extractor will believe it — defeating IP-keyed rate
// limits and poisoning any IP stored for audit. Each trusted proxy hop
// APPENDS the peer it actually saw on the RIGHT, so the real client is
// the entry `trustedHops` from the end, and everything to its left is
// untrusted.
package clientip

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Deployment-wide defaults used by FromRequestDefault — the single
// source of truth for every IP-keyed rate limit and audit-IP capture
// (platform auth routes via internal/auth, and the contact / support /
// sovereignty limiters via internal/ratelimit). One trusted hop is the
// canonical shape (Scaleway LB → gateway); set 2 when an LB and nginx
// both append. Configured once from the environment in main.go.
var (
	TrustProxy  = true
	TrustedHops = 1
)

// ConfigureFromEnv reads PLATFORM_TRUST_PROXY ("false" keys every
// limiter on the TCP peer only) and PLATFORM_TRUSTED_PROXY_HOPS
// (integer ≥ 1). Unset or invalid values keep the defaults above.
func ConfigureFromEnv() {
	if v := strings.TrimSpace(os.Getenv("PLATFORM_TRUST_PROXY")); v != "" {
		TrustProxy = !strings.EqualFold(v, "false")
	}
	if v := strings.TrimSpace(os.Getenv("PLATFORM_TRUSTED_PROXY_HOPS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			TrustedHops = n
		}
	}
}

// FromRequestDefault resolves the client IP using the deployment-wide
// defaults above.
func FromRequestDefault(r *http.Request) string {
	return FromRequest(r, TrustProxy, TrustedHops)
}

// FromRequest returns the client IP.
//
//   - trustProxy=false → the TCP peer only; X-Forwarded-For is ignored.
//   - trustProxy=true  → the entry `trustedHops` from the RIGHT of
//     X-Forwarded-For (the address the outermost trusted proxy saw).
//     trustedHops < 1 is treated as 1. If the header has fewer entries
//     than trustedHops the chain doesn't match the deployment
//     assumption, so this fails CLOSED to the TCP peer rather than
//     trusting whatever leftmost value the client supplied.
//
// With no X-Forwarded-For at all, the TCP peer is returned.
func FromRequest(r *http.Request, trustProxy bool, trustedHops int) string {
	if !trustProxy {
		return RemoteAddrNoPort(r)
	}
	if trustedHops < 1 {
		trustedHops = 1
	}
	entries := SplitXFF(r.Header.Get("X-Forwarded-For"))
	if len(entries) == 0 {
		return RemoteAddrNoPort(r)
	}
	idx := len(entries) - trustedHops
	if idx < 0 {
		return RemoteAddrNoPort(r)
	}
	return entries[idx]
}

// SplitXFF splits an X-Forwarded-For value into trimmed, non-empty
// entries. `"1.2.3.4,,  5.6.7.8"` → ["1.2.3.4", "5.6.7.8"].
func SplitXFF(xff string) []string {
	if xff == "" {
		return nil
	}
	parts := strings.Split(xff, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// RemoteAddrNoPort strips the port from r.RemoteAddr ("1.2.3.4:5678" →
// "1.2.3.4", "[::1]:5678" → "::1"). Uses net.SplitHostPort so a bare
// IPv6 address with no port ("::1") is returned intact rather than
// truncated at its last colon; net/http always supplies host:port, so
// the fallback only matters for hand-built requests.
func RemoteAddrNoPort(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
