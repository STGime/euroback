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
