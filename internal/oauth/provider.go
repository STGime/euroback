package oauth

import (
	"context"
	"fmt"
)

// UserInfo holds the user profile returned by an OAuth provider.
type UserInfo struct {
	Email      string
	Name       string
	AvatarURL  string
	ProviderID string // the user's ID at the provider
}

// AuthURLConfig carries all data needed to build the provider's authorize URL.
//
// Most providers only use ClientID / RedirectURL / State. Microsoft uses
// TenantID to pick the Entra ID authority (single-tenant, multi-tenant, or
// personal-accounts). A zero value for a provider-specific field is always
// safe — providers that don't care simply ignore it.
type AuthURLConfig struct {
	ClientID    string
	RedirectURL string
	State       string

	// Microsoft-specific (ignored by other providers).
	// Empty or "common" → multi-tenant + personal accounts.
	// "organizations"   → work/school accounts only.
	// "consumers"       → personal accounts only.
	// Specific GUID     → lock to a single Entra tenant.
	TenantID string
}

// ExchangeConfig holds all data needed for the OAuth code exchange.
type ExchangeConfig struct {
	ClientID     string
	ClientSecret string // plain secret for most providers; PEM private key for Apple
	Code         string
	RedirectURL  string

	// Apple-specific (ignored by other providers).
	TeamID string
	KeyID  string

	// Microsoft-specific (ignored by other providers). Must match the value
	// used in AuthURLConfig so the token endpoint path is consistent.
	TenantID string
}

// Provider defines the interface for an OAuth identity provider.
type Provider interface {
	Name() string
	AuthURL(cfg AuthURLConfig) string
	ExchangeCode(ctx context.Context, cfg ExchangeConfig) (*UserInfo, error)
}

var providers = map[string]Provider{}

// Register adds a provider to the global registry.
func Register(p Provider) {
	providers[p.Name()] = p
}

// Get returns a registered provider by name.
func Get(name string) (Provider, error) {
	p, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown oauth provider: %s", name)
	}
	return p, nil
}

func init() {
	Register(&GoogleProvider{})
	Register(&GitHubProvider{})
	Register(&LinkedInProvider{})
	Register(&AppleProvider{})
	Register(&MicrosoftProvider{})
}
