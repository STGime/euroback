package oauth

import (
	"strings"
	"testing"
)

func TestProviderRegistry(t *testing.T) {
	expected := []string{"google", "github", "linkedin", "apple", "microsoft"}
	for _, name := range expected {
		p, err := Get(name)
		if err != nil {
			t.Errorf("expected provider %q to be registered, got error: %v", name, err)
			continue
		}
		if p.Name() != name {
			t.Errorf("provider.Name() = %q, want %q", p.Name(), name)
		}
	}
}

func TestUnknownProvider(t *testing.T) {
	_, err := Get("invalid")
	if err == nil {
		t.Error("expected error for unknown provider, got nil")
	}
}

func TestAuthURLDomains(t *testing.T) {
	tests := []struct {
		provider       string
		expectedDomain string
	}{
		{"google", "accounts.google.com"},
		{"github", "github.com"},
		{"linkedin", "www.linkedin.com"},
		{"apple", "appleid.apple.com"},
		{"microsoft", "login.microsoftonline.com"},
	}

	for _, tt := range tests {
		p, err := Get(tt.provider)
		if err != nil {
			t.Fatalf("provider %q not found: %v", tt.provider, err)
		}
		authURL := p.AuthURL(AuthURLConfig{
			ClientID:    "test-client-id",
			RedirectURL: "https://example.com/callback",
			State:       "test-state",
		})
		if !strings.Contains(authURL, tt.expectedDomain) {
			t.Errorf("%s AuthURL = %q, expected to contain %q", tt.provider, authURL, tt.expectedDomain)
		}
	}
}

func TestGoogleAuthURLParams(t *testing.T) {
	p, _ := Get("google")
	u := p.AuthURL(AuthURLConfig{ClientID: "my-client", RedirectURL: "https://example.com/cb", State: "my-state"})
	for _, want := range []string{"client_id=my-client", "redirect_uri=", "state=my-state", "scope=openid+email+profile"} {
		if !strings.Contains(u, want) {
			t.Errorf("Google AuthURL missing %q in %q", want, u)
		}
	}
}

func TestGitHubAuthURLParams(t *testing.T) {
	p, _ := Get("github")
	u := p.AuthURL(AuthURLConfig{ClientID: "gh-client", RedirectURL: "https://example.com/cb", State: "gh-state"})
	for _, want := range []string{"client_id=gh-client", "state=gh-state", "scope=user"} {
		if !strings.Contains(u, want) {
			t.Errorf("GitHub AuthURL missing %q in %q", want, u)
		}
	}
}

func TestLinkedInAuthURLParams(t *testing.T) {
	p, _ := Get("linkedin")
	u := p.AuthURL(AuthURLConfig{ClientID: "li-client", RedirectURL: "https://example.com/cb", State: "li-state"})
	for _, want := range []string{"client_id=li-client", "state=li-state", "scope=openid+profile+email"} {
		if !strings.Contains(u, want) {
			t.Errorf("LinkedIn AuthURL missing %q in %q", want, u)
		}
	}
}

func TestAppleAuthURLParams(t *testing.T) {
	p, _ := Get("apple")
	u := p.AuthURL(AuthURLConfig{ClientID: "apple-client", RedirectURL: "https://example.com/cb", State: "apple-state"})
	for _, want := range []string{"client_id=apple-client", "state=apple-state", "response_mode=form_post", "scope=name+email"} {
		if !strings.Contains(u, want) {
			t.Errorf("Apple AuthURL missing %q in %q", want, u)
		}
	}
}

// TestMicrosoftAuthURLParams verifies the standard OIDC params land in the
// authorize URL and the default tenant is `common` when none configured.
func TestMicrosoftAuthURLParams(t *testing.T) {
	p, _ := Get("microsoft")
	u := p.AuthURL(AuthURLConfig{ClientID: "ms-client", RedirectURL: "https://example.com/cb", State: "ms-state"})
	wants := []string{
		"login.microsoftonline.com/common/oauth2/v2.0/authorize",
		"client_id=ms-client",
		"state=ms-state",
		"scope=openid+email+profile+offline_access",
		"response_type=code",
	}
	for _, want := range wants {
		if !strings.Contains(u, want) {
			t.Errorf("Microsoft AuthURL missing %q in %q", want, u)
		}
	}
}

// TestMicrosoftAuthURLSingleTenant confirms a specific tenant GUID is
// embedded in the authorize URL path — this is the mechanism that locks
// sign-in to a single Entra organisation.
func TestMicrosoftAuthURLSingleTenant(t *testing.T) {
	p, _ := Get("microsoft")
	tenant := "11111111-2222-3333-4444-555555555555"
	u := p.AuthURL(AuthURLConfig{
		ClientID:    "ms-client",
		RedirectURL: "https://example.com/cb",
		State:       "ms-state",
		TenantID:    tenant,
	})
	want := "login.microsoftonline.com/" + tenant + "/oauth2/v2.0/authorize"
	if !strings.Contains(u, want) {
		t.Errorf("Microsoft AuthURL single-tenant missing %q in %q", want, u)
	}
	if strings.Contains(u, "/common/") {
		t.Errorf("Microsoft AuthURL should not fall back to /common/ when tenant configured: %q", u)
	}
}

func TestMicrosoftMultiTenantAliasDetection(t *testing.T) {
	cases := map[string]bool{
		"":                                       true,
		"common":                                 true,
		"COMMON":                                 true,
		"organizations":                          true,
		"consumers":                              true,
		"11111111-2222-3333-4444-555555555555":   false,
		"contoso.onmicrosoft.com":                false,
	}
	for in, want := range cases {
		if got := isMicrosoftMultiTenantAlias(in); got != want {
			t.Errorf("isMicrosoftMultiTenantAlias(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAppleExchangeCodeMissingConfig(t *testing.T) {
	p, _ := Get("apple")
	_, err := p.ExchangeCode(nil, ExchangeConfig{
		ClientID:     "test",
		ClientSecret: "test",
		Code:         "test",
		RedirectURL:  "https://example.com/cb",
		// TeamID and KeyID intentionally empty
	})
	if err == nil {
		t.Error("expected error when TeamID/KeyID missing, got nil")
	}
	if !strings.Contains(err.Error(), "team_id and key_id are required") {
		t.Errorf("unexpected error message: %v", err)
	}
}
