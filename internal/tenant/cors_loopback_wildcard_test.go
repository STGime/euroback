package tenant

import (
	"strings"
	"testing"
)

// Loopback port-wildcard support for per-project cors_origins. Motivating
// case: local dev editors (e.g. ship.studio) bind a fresh random localhost
// port each run, so an exact-match allowlist never keeps up. A single
// "http://localhost:*" entry matches the host on any port. Safe because a
// remote page can never carry a loopback Origin (see
// parseLoopbackPortWildcard).

func TestIsCORSOriginAllowed_LoopbackPortWildcard_MatchesAnyPort(t *testing.T) {
	cfg := &AuthConfig{CORSOrigins: []string{
		"http://localhost:*",
		"http://127.0.0.1:*",
		"http://[::1]:*",
		"https://localhost:*",
	}}
	allowed := []string{
		"http://localhost:3000",
		"http://localhost:54321",
		"http://localhost", // default-port origin, no explicit :port
		"http://127.0.0.1:8080",
		"http://[::1]:5173",
		"https://localhost:443",
		"https://localhost:61234",
	}
	for _, origin := range allowed {
		if !cfg.IsCORSOriginAllowed(origin) {
			t.Errorf("expected loopback wildcard to allow %q", origin)
		}
	}
}

func TestIsCORSOriginAllowed_LoopbackPortWildcard_RejectsLookalikesAndRemotes(t *testing.T) {
	cfg := &AuthConfig{CORSOrigins: []string{"http://localhost:*"}}
	rejected := []string{
		"http://localhost.evil.com",      // suffix look-alike, not loopback
		"http://localhost.evil.com:3000", // same, with a port
		"http://notlocalhost:3000",       // prefix look-alike
		"http://localhost5000",           // digit-suffixed host, no ':' boundary — must not match
		"http://localhost.evil:3000",     // extended host with a real port
		"http://localhost:3000.evil.com", // non-numeric "port" tail
		"http://localhost:",              // empty port tail
		"https://localhost:3000",         // scheme mismatch (wildcard is http)
		"http://127.0.0.1:3000",          // different loopback spelling not listed
		"http://[::1]:3000",              // different loopback spelling not listed
		"https://app.example.com",        // wholly unrelated remote
		"",                               // empty origin
	}
	for _, origin := range rejected {
		if cfg.IsCORSOriginAllowed(origin) {
			t.Errorf("expected %q to be REJECTED by http://localhost:* wildcard", origin)
		}
	}
}

// A port wildcard is loopback-only by design: a wildcard on a routable
// host would let any browser origin on that host drive credentialed calls,
// which is exactly the remote exposure the loopback restriction avoids.
func TestIsCORSOriginAllowed_PortWildcard_NonLoopbackNotHonored(t *testing.T) {
	cfg := &AuthConfig{CORSOrigins: []string{"https://app.example.com:*"}}
	// The entry isn't recognized as a loopback wildcard, so it falls back
	// to exact match and never matches a real (ported) origin.
	for _, origin := range []string{
		"https://app.example.com:3000",
		"https://app.example.com:8443",
		"https://app.example.com",
	} {
		if cfg.IsCORSOriginAllowed(origin) {
			t.Errorf("non-loopback port wildcard must NOT match %q", origin)
		}
	}
}

func TestValidate_AcceptsLoopbackPortWildcards(t *testing.T) {
	cfg := &AuthConfig{
		Providers:         map[string]ProviderConfig{"email_password": {Enabled: true}},
		PasswordMinLength: 8,
		SessionDuration:   "168h",
		CORSOrigins: []string{
			"http://localhost:*",
			"http://127.0.0.1:*",
			"http://[::1]:*",
			"https://localhost:*",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate rejected valid loopback port wildcards: %v", err)
	}
}

// A "*" port on a non-loopback host is not a recognized wildcard, so it
// must fail the normal URL parse (":*" isn't a valid port) rather than be
// silently accepted-but-ignored.
func TestValidate_RejectsNonLoopbackPortWildcard(t *testing.T) {
	cfg := &AuthConfig{
		Providers:         map[string]ProviderConfig{"email_password": {Enabled: true}},
		PasswordMinLength: 8,
		SessionDuration:   "168h",
		CORSOrigins:       []string{"https://app.example.com:*"},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate should reject a non-loopback port wildcard (https://app.example.com:*)")
	}
}

func TestParseLoopbackPortWildcard_Table(t *testing.T) {
	cases := []struct {
		in         string
		wantPrefix string
		wantOK     bool
	}{
		{"http://localhost:*", "http://localhost", true},
		{"https://localhost:*", "https://localhost", true},
		{"http://127.0.0.1:*", "http://127.0.0.1", true},
		{"http://[::1]:*", "http://[::1]", true},
		{"  http://localhost:*  ", "http://localhost", true}, // trimmed
		{"http://localhost:3000", "", false},                 // concrete port, not a wildcard
		{"http://localhost", "", false},                      // no :* suffix
		{"ftp://localhost:*", "", false},                     // scheme not http(s)
		{"http://example.com:*", "", false},                  // non-loopback host
		{"http://localhost.evil.com:*", "", false},           // look-alike host
		{"*", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		gotPrefix, gotOK := parseLoopbackPortWildcard(tc.in)
		if gotOK != tc.wantOK || gotPrefix != tc.wantPrefix {
			t.Errorf("parseLoopbackPortWildcard(%q) = (%q, %v), want (%q, %v)",
				tc.in, gotPrefix, gotOK, tc.wantPrefix, tc.wantOK)
		}
	}
}

// Guard against the pre-existing silent-accept-but-ignore trap for host
// wildcards: "https://*.example.com" parses as a URL so Validate accepts
// it, but the matcher is exact-only, so it never actually matches. This
// test documents that current behavior so a future change to honor host
// wildcards has to update it deliberately (and add the guardrails).
func TestIsCORSOriginAllowed_HostWildcardStillInert(t *testing.T) {
	cfg := &AuthConfig{CORSOrigins: []string{"https://*.example.com"}}
	if cfg.IsCORSOriginAllowed("https://app.example.com") {
		t.Error("host wildcard is not yet supported in per-project CORS; " +
			"if this now matches, add public-suffix / platform-domain guardrails")
	}
	if !strings.HasSuffix("https://*.example.com", ".example.com") {
		t.Fatal("sanity")
	}
}
