package tenant

import "testing"

// #258: the email-flow resolver contract. Broken here → tenant signups
// fall through to either the pre-#258 console-URL default (which 404s)
// or an unhandled nil-URL crash. This file pins the shape.

func TestResolveEmailRedirect_PerRequestWinsOverDefault(t *testing.T) {
	cfg := AuthConfig{
		RedirectURLs: []string{
			"https://app.example.com/verify",
			"https://app.example.com/verify-alt",
		},
		EmailVerificationURL: "https://app.example.com/verify",
	}
	got, ok := cfg.ResolveEmailRedirect(EmailFlowVerification, "https://app.example.com/verify-alt")
	if !ok {
		t.Fatal("expected ok=true when per-request is in allowlist")
	}
	if got != "https://app.example.com/verify-alt" {
		t.Errorf("per-request override lost: got %q", got)
	}
}

func TestResolveEmailRedirect_PerRequestNotInAllowlistFails(t *testing.T) {
	cfg := AuthConfig{
		RedirectURLs:         []string{"https://app.example.com/verify"},
		EmailVerificationURL: "https://app.example.com/verify",
	}
	// An attacker-controlled URL that happens to be a valid HTTPS URL
	// but is NOT in the tenant's allowlist must be rejected. This is
	// the open-redirect defence.
	_, ok := cfg.ResolveEmailRedirect(EmailFlowVerification, "https://evil.example.com/verify")
	if ok {
		t.Fatal("resolver accepted a URL not in redirect_urls — open-redirect regression")
	}
}

func TestResolveEmailRedirect_FallbackToDefault(t *testing.T) {
	cfg := AuthConfig{
		RedirectURLs:         []string{"https://app.example.com/verify"},
		EmailVerificationURL: "https://app.example.com/verify",
	}
	got, ok := cfg.ResolveEmailRedirect(EmailFlowVerification, "")
	if !ok {
		t.Fatal("expected ok=true when default is set and per-request is empty")
	}
	if got != "https://app.example.com/verify" {
		t.Errorf("default lost: got %q", got)
	}
}

func TestResolveEmailRedirect_NoDefaultAndNoOverrideFails(t *testing.T) {
	cfg := AuthConfig{
		RedirectURLs: []string{"https://app.example.com/verify"},
		// EmailVerificationURL intentionally empty.
	}
	got, ok := cfg.ResolveEmailRedirect(EmailFlowVerification, "")
	if ok {
		t.Fatalf("expected ok=false when nothing is configured, got %q", got)
	}
}

func TestResolveEmailRedirect_PerFlowIndependence(t *testing.T) {
	// A tenant may have verification configured but not magic-link.
	// The resolver must return ok=false only for the unconfigured
	// flow, not conflate them.
	cfg := AuthConfig{
		RedirectURLs:         []string{"https://app.example.com/verify"},
		EmailVerificationURL: "https://app.example.com/verify",
		// PasswordResetURL + MagicLinkURL intentionally empty.
	}
	if _, ok := cfg.ResolveEmailRedirect(EmailFlowVerification, ""); !ok {
		t.Error("verification: expected ok=true")
	}
	if _, ok := cfg.ResolveEmailRedirect(EmailFlowPasswordReset, ""); ok {
		t.Error("password_reset: expected ok=false (not configured)")
	}
	if _, ok := cfg.ResolveEmailRedirect(EmailFlowMagicLink, ""); ok {
		t.Error("magic_link: expected ok=false (not configured)")
	}
}

func TestValidate_RejectsRedirectURLNotInAllowlist(t *testing.T) {
	cfg := AuthConfig{
		Providers:                map[string]ProviderConfig{"email_password": {Enabled: true}},
		PasswordMinLength:        8,
		SessionDuration:          "168h",
		RedirectURLs:             []string{"https://app.example.com/verify"},
		EmailVerificationURL:     "https://different-host.example.com/verify",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted email_verification_url not in redirect_urls")
	}
}

// TestUrlsMatch_CaseInsensitiveSchemeAndHost pins the RFC 3986 §3.2.2
// contract that browsers already follow: a tenant that stores
// "https://App.Example.com/verify" and calls with the lowercased
// variant should match. Was a ship-blocker on the #262 review.
func TestUrlsMatch_CaseInsensitiveSchemeAndHost(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// Case-insensitive scheme + host.
		{"https://App.Example.com/verify", "https://app.example.com/verify", true},
		{"HTTPS://app.example.com/verify", "https://app.example.com/verify", true},
		// Case-sensitive path — a genuine path mismatch stays a
		// mismatch even under the case-insensitive host rule.
		{"https://app.example.com/Verify", "https://app.example.com/verify", false},
		// Different hosts — not a match.
		{"https://evil.example.com/verify", "https://app.example.com/verify", false},
		// Different schemes — not a match (open-redirect defence).
		{"http://app.example.com/verify", "https://app.example.com/verify", false},
		// Query preserved case-sensitive.
		{"https://app.example.com/verify?flow=A", "https://app.example.com/verify?flow=a", false},
		// Fragments preserved case-sensitive.
		{"https://app.example.com/#/Verify", "https://app.example.com/#/verify", false},
		// Custom scheme (myapp://) — fallback plain-string compare
		// still works.
		{"myapp://verify", "myapp://verify", true},
		{"myapp://verify", "MYAPP://verify", true},
	}
	for _, c := range cases {
		if got := urlsMatch(c.a, c.b); got != c.want {
			t.Errorf("urlsMatch(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestIsInRedirectAllowlist_CaseInsensitiveHost exercises the
// integration between AuthConfig.isInRedirectAllowlist and urlsMatch
// end-to-end.
func TestIsInRedirectAllowlist_CaseInsensitiveHost(t *testing.T) {
	cfg := AuthConfig{
		RedirectURLs: []string{"https://App.Example.com/verify"},
	}
	// Lowercased host must match — the whole point.
	if !cfg.isInRedirectAllowlist("https://app.example.com/verify") {
		t.Error("lowercase host should match stored mixed-case host")
	}
	// Extra whitespace on either side — trimmed.
	if !cfg.isInRedirectAllowlist("  https://app.example.com/verify  ") {
		t.Error("trimmed whitespace should match")
	}
	// Different path must NOT match.
	if cfg.isInRedirectAllowlist("https://app.example.com/other") {
		t.Error("different path must not match — open-redirect defence")
	}
}

// TestValidate_AcceptsRedirectURLInAllowlist pins the happy path so a
// future change to Validate can't accidentally break tenants who
// configured everything correctly. Suggested by review #262.
func TestValidate_AcceptsRedirectURLInAllowlist(t *testing.T) {
	cfg := AuthConfig{
		Providers:            map[string]ProviderConfig{"email_password": {Enabled: true}},
		PasswordMinLength:    8,
		SessionDuration:      "168h",
		RedirectURLs:         []string{"https://app.example.com/verify", "https://app.example.com/reset", "https://app.example.com/magic"},
		EmailVerificationURL: "https://app.example.com/verify",
		PasswordResetURL:     "https://app.example.com/reset",
		MagicLinkURL:         "https://app.example.com/magic",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate rejected a well-formed config: %v", err)
	}
}

func TestValidate_EmptyURLsAllowedByValidate(t *testing.T) {
	// Validate is the "does the config parse cleanly?" check, not the
	// "is email confirmation actually usable?" check. A tenant may
	// legitimately store the config with the URLs empty (email
	// confirmation off, or configured later). The Signup path is
	// where the fail-loud on "confirmation on + no URL" fires.
	cfg := AuthConfig{
		Providers:         map[string]ProviderConfig{"email_password": {Enabled: true}},
		PasswordMinLength: 8,
		SessionDuration:   "168h",
		RedirectURLs:      []string{"https://app.example.com/"},
		// All three email-flow URLs empty — should Validate cleanly.
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate rejected empty URLs: %v", err)
	}
}
