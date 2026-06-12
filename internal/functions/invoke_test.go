package functions

import (
	"net/http"
	"testing"
)

// Issue #214: the gateway must forward the caller's custom request headers
// to the runner (webhook/API auth via X-Signature, X-Api-Key, …) while
// never leaking platform auth or letting a caller spoof the signed
// gateway control headers.

func TestCopyForwardableHeaders_ForwardsCustomStripsControlAndAuth(t *testing.T) {
	src := http.Header{}
	// custom headers a webhook/API would send — must be forwarded
	src.Set("X-Feed-Key", "secret123")
	src.Set("X-Signature", "abc")
	src.Set("X-Api-Key", "k")
	src.Set("X-Webhook-Id", "wh_1")
	src.Set("Content-Type", "application/xml")
	src.Set("User-Agent", "PartnerBot/1.0")
	src.Add("X-Multi", "a")
	src.Add("X-Multi", "b")
	// platform auth — must be stripped
	src.Set("Authorization", "Bearer anon-key")
	src.Set("apikey", "anon-key")
	src.Set("Cookie", "session=…")
	// gateway control namespace — must be stripped (caller cannot spoof)
	src.Set("X-Project-ID", "attacker-project")
	src.Set("X-User-ID", "attacker-user")
	src.Set("X-Request-ID", "forged")          // would break HMAC if forwarded
	src.Set("X-Eurobase-Signature", "forged")
	src.Set("X-Function-Version", "999")
	// hop-by-hop — must be stripped
	src.Set("Connection", "keep-alive")
	src.Set("Transfer-Encoding", "chunked")
	src.Set("Host", "evil.example")

	dst := http.Header{}
	copyForwardableHeaders(dst, src)

	forwarded := []string{"X-Feed-Key", "X-Signature", "X-Api-Key", "X-Webhook-Id", "Content-Type", "User-Agent"}
	for _, h := range forwarded {
		if dst.Get(h) == "" {
			t.Errorf("expected %s to be forwarded, but it was dropped", h)
		}
	}
	if got := dst.Values("X-Multi"); len(got) != 2 {
		t.Errorf("expected multi-valued X-Multi to forward both values, got %v", got)
	}

	stripped := []string{
		"Authorization", "apikey", "Cookie",
		"X-Project-ID", "X-User-ID", "X-Request-ID", "X-Eurobase-Signature", "X-Function-Version",
		"Connection", "Transfer-Encoding", "Host",
	}
	for _, h := range stripped {
		if dst.Get(h) != "" {
			t.Errorf("SECURITY/PROTOCOL: %s must NOT be forwarded, but got %q", h, dst.Get(h))
		}
	}
}

func TestIsGatewayControlHeader(t *testing.T) {
	for _, h := range []string{
		"x-eurobase-signature", "x-eurobase-timestamp",
		"x-project-id", "x-schema-name", "x-plan",
		"x-function-id", "x-function-name", "x-function-version",
		"x-user-id", "x-user-email", "x-request-id",
	} {
		if !isGatewayControlHeader(h) {
			t.Errorf("%s should be a gateway control header", h)
		}
	}
	for _, h := range []string{"x-signature", "x-api-key", "x-feed-key", "content-type", "x-webhook-id"} {
		if isGatewayControlHeader(h) {
			t.Errorf("%s is a caller header, must be forwardable", h)
		}
	}
}
