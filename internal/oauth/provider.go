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

// ExchangeConfig holds all data needed for the OAuth code exchange.
type ExchangeConfig struct {
	ClientID     string
	ClientSecret string // plain secret for most providers; PEM private key for Apple
	Code         string
	RedirectURL  string
	// Apple-specific (ignored by other providers)
	TeamID string
	KeyID  string
}

// Provider defines the interface for an OAuth identity provider.
type Provider interface {
	Name() string
	AuthURL(clientID, redirectURL, state string) string
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
}
