package tenant

import (
	"strings"
	"testing"
)

// IsCORSOriginAllowed: per-project tenant-controlled CORS allowlist.

func TestIsCORSOriginAllowed_ExactMatch(t *testing.T) {
	cfg := &AuthConfig{CORSOrigins: []string{
		"http://localhost:3000",
		"https://app.example.com",
		"http://localhost:5173",
	}}
	for _, origin := range []string{"http://localhost:3000", "https://app.example.com", "http://localhost:5173"} {
		if !cfg.IsCORSOriginAllowed(origin) {
			t.Errorf("expected %q to be allowed", origin)
		}
	}
}

func TestIsCORSOriginAllowed_RejectsMismatches(t *testing.T) {
	cfg := &AuthConfig{CORSOrigins: []string{"https://app.example.com"}}
	for _, origin := range []string{
		"https://attacker.example.com",
		"http://app.example.com", // scheme mismatch
		"https://app.example.com:8443", // port mismatch
		"https://other.com",
		"https://app.example.com/path", // origin must not include path
		"",
	} {
		if cfg.IsCORSOriginAllowed(origin) {
			t.Errorf("expected %q to be REJECTED, got allowed", origin)
		}
	}
}

func TestIsCORSOriginAllowed_TrailingSlashTolerantOnConfig(t *testing.T) {
	// Operators sometimes paste `https://app.example.com/` with a slash.
	// Browser Origin headers never have a trailing slash, so we
	// normalise the config side at compare time.
	cfg := &AuthConfig{CORSOrigins: []string{"https://app.example.com/"}}
	if !cfg.IsCORSOriginAllowed("https://app.example.com") {
		t.Error("expected configured trailing-slash entry to match no-slash origin")
	}
}

func TestIsCORSOriginAllowed_EmptyListAllowsNothing(t *testing.T) {
	cfg := &AuthConfig{}
	for _, origin := range []string{"http://localhost:3000", "https://app.example.com"} {
		if cfg.IsCORSOriginAllowed(origin) {
			t.Errorf("empty list should allow nothing, got %q allowed", origin)
		}
	}
}

func TestValidate_AcceptsValidCORSOrigins(t *testing.T) {
	cfg := &AuthConfig{
		Providers:                map[string]ProviderConfig{"email_password": {Enabled: true}},
		PasswordMinLength:        8,
		SessionDuration:          "168h",
		CORSOrigins:              []string{"http://localhost:3000", "https://app.example.com:8443"},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate rejected good cors_origins: %v", err)
	}
}

func TestValidate_RejectsCORSOriginWithPath(t *testing.T) {
	cfg := &AuthConfig{
		Providers:                map[string]ProviderConfig{"email_password": {Enabled: true}},
		PasswordMinLength:        8,
		SessionDuration:          "168h",
		CORSOrigins:              []string{"https://app.example.com/cb"},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("Validate should reject cors_origin with path; got %v", err)
	}
}

func TestValidate_RejectsCORSOriginWithoutScheme(t *testing.T) {
	cfg := &AuthConfig{
		Providers:                map[string]ProviderConfig{"email_password": {Enabled: true}},
		PasswordMinLength:        8,
		SessionDuration:          "168h",
		CORSOrigins:              []string{"app.example.com"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("Validate should reject cors_origin without scheme")
	}
}

func TestValidate_RejectsCORSOriginWithQueryOrFragment(t *testing.T) {
	for _, bad := range []string{
		"https://app.example.com?foo=bar",
		"https://app.example.com#frag",
	} {
		cfg := &AuthConfig{
			Providers:                map[string]ProviderConfig{"email_password": {Enabled: true}},
			PasswordMinLength:        8,
			SessionDuration:          "168h",
			CORSOrigins:              []string{bad},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate should reject %q", bad)
		}
	}
}
