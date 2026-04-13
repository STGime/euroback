package oauth

import (
	"strings"
	"testing"
)

func TestProviderRegistry(t *testing.T) {
	expected := []string{"google", "github", "linkedin", "apple"}
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
	}

	for _, tt := range tests {
		p, err := Get(tt.provider)
		if err != nil {
			t.Fatalf("provider %q not found: %v", tt.provider, err)
		}
		authURL := p.AuthURL("test-client-id", "https://example.com/callback", "test-state")
		if !strings.Contains(authURL, tt.expectedDomain) {
			t.Errorf("%s AuthURL = %q, expected to contain %q", tt.provider, authURL, tt.expectedDomain)
		}
	}
}

func TestGoogleAuthURLParams(t *testing.T) {
	p, _ := Get("google")
	u := p.AuthURL("my-client", "https://example.com/cb", "my-state")
	for _, want := range []string{"client_id=my-client", "redirect_uri=", "state=my-state", "scope=openid+email+profile"} {
		if !strings.Contains(u, want) {
			t.Errorf("Google AuthURL missing %q in %q", want, u)
		}
	}
}

func TestGitHubAuthURLParams(t *testing.T) {
	p, _ := Get("github")
	u := p.AuthURL("gh-client", "https://example.com/cb", "gh-state")
	for _, want := range []string{"client_id=gh-client", "state=gh-state", "scope=user"} {
		if !strings.Contains(u, want) {
			t.Errorf("GitHub AuthURL missing %q in %q", want, u)
		}
	}
}

func TestLinkedInAuthURLParams(t *testing.T) {
	p, _ := Get("linkedin")
	u := p.AuthURL("li-client", "https://example.com/cb", "li-state")
	for _, want := range []string{"client_id=li-client", "state=li-state", "scope=openid+profile+email"} {
		if !strings.Contains(u, want) {
			t.Errorf("LinkedIn AuthURL missing %q in %q", want, u)
		}
	}
}

func TestAppleAuthURLParams(t *testing.T) {
	p, _ := Get("apple")
	u := p.AuthURL("apple-client", "https://example.com/cb", "apple-state")
	for _, want := range []string{"client_id=apple-client", "state=apple-state", "response_mode=form_post", "scope=name+email"} {
		if !strings.Contains(u, want) {
			t.Errorf("Apple AuthURL missing %q in %q", want, u)
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
