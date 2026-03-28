package tenant

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ProviderConfig defines settings for a single auth provider.
type ProviderConfig struct {
	Enabled bool `json:"enabled"`
}

// OAuthProviderConfig holds the settings for a social login provider.
type OAuthProviderConfig struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// AuthConfig holds the per-project authentication configuration.
type AuthConfig struct {
	Providers                map[string]ProviderConfig        `json:"providers"`
	OAuthProviders           map[string]OAuthProviderConfig   `json:"oauth_providers,omitempty"`
	PasswordMinLength        int                              `json:"password_min_length"`
	RequireEmailConfirmation bool                             `json:"require_email_confirmation"`
	SessionDuration          string                           `json:"session_duration"`
	RedirectURLs             []string                         `json:"redirect_urls"`
}

// allowedSessionDurations is the set of valid session duration values.
var allowedSessionDurations = map[string]bool{
	"1h":   true,
	"24h":  true,
	"168h": true,
	"720h": true,
}

// DefaultAuthConfig returns the default auth configuration for new projects.
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		Providers: map[string]ProviderConfig{
			"email_password": {Enabled: true},
		},
		PasswordMinLength:        8,
		RequireEmailConfirmation: false,
		SessionDuration:          "168h",
		RedirectURLs:             []string{"http://localhost:3000"},
	}
}

// Validate checks that the AuthConfig values are within allowed bounds.
func (c *AuthConfig) Validate() error {
	if c.PasswordMinLength < 8 || c.PasswordMinLength > 128 {
		return fmt.Errorf("password_min_length must be between 8 and 128")
	}

	if !allowedSessionDurations[c.SessionDuration] {
		return fmt.Errorf("session_duration must be one of: 1h, 24h, 168h, 720h")
	}

	for _, rawURL := range c.RedirectURLs {
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid redirect URL: %s", rawURL)
		}
	}

	hasEnabled := false
	for _, p := range c.Providers {
		if p.Enabled {
			hasEnabled = true
			break
		}
	}
	if !hasEnabled {
		for _, p := range c.OAuthProviders {
			if p.Enabled && p.ClientID != "" && p.ClientSecret != "" {
				hasEnabled = true
				break
			}
		}
	}
	if !hasEnabled {
		return fmt.Errorf("at least one auth provider must be enabled")
	}

	return nil
}

// SessionDurationSeconds parses the session duration string and returns seconds.
func (c *AuthConfig) SessionDurationSeconds() int {
	d, err := time.ParseDuration(c.SessionDuration)
	if err != nil {
		return 3600 // fallback to 1 hour
	}
	return int(d.Seconds())
}

// IsEmailPasswordEnabled returns whether the email_password provider is enabled.
func (c *AuthConfig) IsEmailPasswordEnabled() bool {
	p, ok := c.Providers["email_password"]
	return ok && p.Enabled
}

// IsMagicLinkEnabled returns whether the magic_link provider is enabled.
func (c *AuthConfig) IsMagicLinkEnabled() bool {
	p, ok := c.Providers["magic_link"]
	return ok && p.Enabled
}

// GetOAuthProvider returns the OAuth provider config if it exists, is enabled, and has credentials.
func (c *AuthConfig) GetOAuthProvider(name string) (OAuthProviderConfig, bool) {
	if c.OAuthProviders == nil {
		return OAuthProviderConfig{}, false
	}
	p, ok := c.OAuthProviders[name]
	return p, ok && p.Enabled && p.ClientID != "" && p.ClientSecret != ""
}

// IsRedirectURLAllowed checks whether the given URL is in the allowed redirect list.
func (c *AuthConfig) IsRedirectURLAllowed(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	candidate := u.Scheme + "://" + u.Host + u.Path
	for _, allowed := range c.RedirectURLs {
		a, err := url.Parse(allowed)
		if err != nil {
			continue
		}
		// Match scheme + host. If allowed URL has a path, the candidate must start with it.
		if a.Scheme == u.Scheme && a.Host == u.Host {
			if a.Path == "" || a.Path == "/" || strings.HasPrefix(candidate, a.Scheme+"://"+a.Host+a.Path) {
				return true
			}
		}
	}
	return false
}

// MaskSecretsJSON takes raw auth_config JSON and masks OAuth client_secret values.
// Returns the masked JSON. Used before returning project data in API responses.
func MaskSecretsJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return raw
	}
	if oauthRaw, ok := cfg["oauth_providers"]; ok {
		if oauth, ok := oauthRaw.(map[string]interface{}); ok {
			for providerName, providerRaw := range oauth {
				if provider, ok := providerRaw.(map[string]interface{}); ok {
					if secret, ok := provider["client_secret"].(string); ok && len(secret) > 8 {
						provider["client_secret"] = secret[:4] + strings.Repeat("*", len(secret)-8) + secret[len(secret)-4:]
					} else if ok && len(secret) > 0 {
						provider["client_secret"] = strings.Repeat("*", len(secret))
					}
					oauth[providerName] = provider
				}
			}
			cfg["oauth_providers"] = oauth
		}
	}
	masked, err := json.Marshal(cfg)
	if err != nil {
		return raw
	}
	return masked
}

// ParseAuthConfig parses a JSON string into an AuthConfig, returning defaults if empty.
func ParseAuthConfig(raw []byte) AuthConfig {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "{}" || strings.TrimSpace(string(raw)) == "null" {
		return DefaultAuthConfig()
	}

	var cfg AuthConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return DefaultAuthConfig()
	}

	// Fill in zero values with defaults.
	defaults := DefaultAuthConfig()
	if cfg.PasswordMinLength == 0 {
		cfg.PasswordMinLength = defaults.PasswordMinLength
	}
	if cfg.SessionDuration == "" {
		cfg.SessionDuration = defaults.SessionDuration
	}
	if cfg.Providers == nil {
		cfg.Providers = defaults.Providers
	}

	return cfg
}
