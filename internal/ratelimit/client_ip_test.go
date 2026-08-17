package ratelimit

import (
	"net/http/httptest"
	"testing"
)

// #228 + #238: ClientIPForProject is the trust-proxy-aware sibling of
// ClientIP. Load-bearing properties the per-project gates depend on:
//
//  1. trustProxy=false → X-Forwarded-For is IGNORED entirely (even
//     when present), TCP peer wins. The anti-XFF-forgery side of the
//     knob. Same behaviour as pre-#238.
//  2. trustProxy=true → **trusted-hop-count extraction**. Pick the
//     entry at index `len(entries) - trustedHops`. Client-forged
//     entries in the leftmost positions get pushed out of the trusted
//     window. This is the #238 hardening.
//  3. No X-Forwarded-For header, either mode → TCP peer.
//  4. FEWER XFF entries than trustedHops → fail-closed to TCP peer
//     (a misconfigured or attack-manipulated chain must not fall back
//     to leftmost). This is the security-critical property; a
//     regression here re-opens the pre-#238 spoof surface.
//  5. trustedHops ≤ 0 is clamped to 1 (belt + suspenders on the
//     merge in EffectiveRateLimits, which also normalises).

func TestClientIPForProject_TrustProxyOff_IgnoresXFF(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.5")

	if got := ClientIPForProject(r, false, 1); got != "10.0.0.5" {
		t.Errorf("trustProxy=false must use TCP peer (no XFF), got %q", got)
	}
	// Even with a wildly wrong trustedHops, TCP peer must win when
	// TrustProxy is off — a config typo mustn't accidentally enable
	// XFF trust.
	if got := ClientIPForProject(r, false, 99); got != "10.0.0.5" {
		t.Errorf("trustProxy=false + hops=99 must still use TCP peer, got %q", got)
	}
}

func TestClientIPForProject_NoXFFFallsBackToTCPPeer(t *testing.T) {
	for _, trustProxy := range []bool{true, false} {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = "192.0.2.42:9999"
		if got := ClientIPForProject(r, trustProxy, 1); got != "192.0.2.42" {
			t.Errorf("no XFF + trustProxy=%v: expected TCP peer 192.0.2.42, got %q", trustProxy, got)
		}
	}
}

// Single-hop chain (current Eurobase prod shape: nginx-ingress with
// use-forwarded-headers=false writes one XFF entry containing the real
// client). Extractor with trustedHops=1 picks that entry.
func TestClientIPForProject_SingleHop(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")

	if got := ClientIPForProject(r, true, 1); got != "198.51.100.1" {
		t.Errorf("single-entry XFF + hops=1 should return that entry, got %q", got)
	}
}

// Two-hop chain (LB + nginx both append). Real client at position
// len-hops = 3-2 = 1 = "203.0.113.7". Leftmost "1.2.3.4" is the
// client-forged prepend and must NOT be returned.
func TestClientIPForProject_TwoHops_ClientForgedEntryDiscarded(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.244.1.5:54321" // nginx pod IP as seen by gateway
	// Attacker prepended "1.2.3.4"; LB appended real client
	// "203.0.113.7"; nginx appended LB pod IP "10.0.0.1".
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.7, 10.0.0.1")

	got := ClientIPForProject(r, true, 2)
	if got != "203.0.113.7" {
		t.Errorf("2-hop extraction should discard leftmost client-forged entry and return real client, got %q", got)
	}
	// The forged entry must NEVER be returned regardless of how many
	// entries the attacker prepends.
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2, 3.3.3.3, 203.0.113.7, 10.0.0.1")
	if got := ClientIPForProject(r, true, 2); got != "203.0.113.7" {
		t.Errorf("2-hop extraction with 5 total entries should still return real client at len-2, got %q", got)
	}
}

// This is the security-critical fail-closed case: if the observed
// chain is SHORTER than the expected trusted-hop count (attacker
// stripped XFF entries, or the request bypassed our LB), the
// extractor must NOT trust whatever leftmost entry happens to be
// present. It must return the TCP peer instead.
//
// A pre-#238 leftmost implementation would return the attacker's
// forged entry here — that's the specific regression this test guards.
func TestClientIPForProject_FewerEntriesThanHops_FailsClosed(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.244.1.5:54321"
	// Only 1 entry present, but config says we expect 2 trusted hops.
	// The entry is untrustworthy in that chain.
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	got := ClientIPForProject(r, true, 2)
	if got == "1.2.3.4" {
		t.Fatalf("fewer entries than hops must NOT return the forged entry — this is the #238 regression guard")
	}
	if got != "10.244.1.5" {
		t.Errorf("fail-closed should return TCP peer 10.244.1.5, got %q", got)
	}
}

// Explicit ≤0 for trustedHops must be clamped to 1, otherwise a
// misconfigured project (or a bug in the merge) could index at
// len-0 = last entry, which is our own nginx pod IP — an availability
// issue, but also potentially a bypass primitive if paired with other
// config drift.
func TestClientIPForProject_TrustedHopsClampedToOne(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")

	for _, hops := range []int{0, -1, -99} {
		if got := ClientIPForProject(r, true, hops); got != "203.0.113.7" {
			t.Errorf("hops=%d should be clamped to 1, got %q (want 203.0.113.7)", hops, got)
		}
	}
}

// splitXFF is the parser under both hops-based extraction and any
// future XFF-aware helper. Pin the empty-entry-drop behaviour: a
// malformed header like `"1.2.3.4,,5.6.7.8"` must produce 2 entries,
// not 3, or an attacker inserting empty commas could shift the
// trusted-hop index.
func TestSplitXFF(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"1.2.3.4", []string{"1.2.3.4"}},
		{"1.2.3.4, 5.6.7.8", []string{"1.2.3.4", "5.6.7.8"}},
		{" 1.2.3.4 , 5.6.7.8 ", []string{"1.2.3.4", "5.6.7.8"}},
		{"1.2.3.4,\t5.6.7.8", []string{"1.2.3.4", "5.6.7.8"}},
		// Empty entries dropped — attacker cannot inflate the entry
		// count with `,,` to shift the trusted-hop window.
		{"1.2.3.4,,5.6.7.8", []string{"1.2.3.4", "5.6.7.8"}},
		{",1.2.3.4", []string{"1.2.3.4"}},
		{"1.2.3.4,", []string{"1.2.3.4"}},
		{",,,", nil},
	}
	for _, c := range cases {
		got := splitXFF(c.in)
		if !equalStringSlice(got, c.want) {
			t.Errorf("splitXFF(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Snapshot the legacy ClientIP behaviour — confirms neither #228 nor
// #238 regresses the platform-wide helper (still used in router.go
// for the audit log's client IP and for legacy auth helpers). Legacy
// callers key on leftmost XFF because they historically had no config
// knob; a future migration would replace them.
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
