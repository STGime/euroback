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
//
// ClientSecret is only populated on incoming requests (when the user sets or
// changes the secret). The secret is never persisted to auth_config JSONB —
// it is stored encrypted in the project vault at key "oauth.{provider}.client_secret".
// SecretSet indicates whether a secret exists in the vault (used in API responses
// so the UI can display "secret configured" without ever sending the value).
type OAuthProviderConfig struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	SecretSet    bool   `json:"secret_set,omitempty"`
	// Apple-specific fields (ignored by other providers).
	TeamID string `json:"team_id,omitempty"`
	KeyID  string `json:"key_id,omitempty"`
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
		// For OAuth providers we only require a client_id to be set; the
		// secret lives in the vault and may not be present in this struct.
		for _, p := range c.OAuthProviders {
			if p.Enabled && p.ClientID != "" {
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

// IsMaskedSecret returns true if the given string looks like a masked value
// returned by MaskSecretsJSON (i.e., contains asterisks). Used by the update
// handler to detect when the console is echoing back a masked value and
// preserve the vault entry instead of overwriting it.
func IsMaskedSecret(s string) bool {
	return strings.Contains(s, "*")
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

// GetOAuthProvider returns the OAuth provider config if it exists and is enabled
// with a client_id configured. The client_secret is NOT checked here — it lives
// in the vault and is loaded separately by the auth service during the OAuth
// code exchange. Callers must fetch the secret from the vault using the project
// schema and the key "oauth.{provider}.client_secret".
func (c *AuthConfig) GetOAuthProvider(name string) (OAuthProviderConfig, bool) {
	if c.OAuthProviders == nil {
		return OAuthProviderConfig{}, false
	}
	p, ok := c.OAuthProviders[name]
	return p, ok && p.Enabled && p.ClientID != ""
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

// MaskSecretsJSON takes raw auth_config JSON and removes any client_secret
// values that may still be present (legacy rows) and replaces them with a
// secret_set boolean. After the OAuth secret migration, auth_config JSONB
// should never contain a client_secret at all — this function is a safety
// net for any pre-migration rows. Used before returning project data in
// API responses.
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
					if _, hasSecret := provider["client_secret"]; hasSecret {
						delete(provider, "client_secret")
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

// AnnotateOAuthSecretStatus accepts auth_config JSON and a lookup function
// that returns whether a given provider has a vault entry. It decorates the
// oauth_providers map entries with "secret_set": true/false so the UI can
// show "Secret configured" without ever fetching the actual secret.
func AnnotateOAuthSecretStatus(raw []byte, hasSecret func(provider string) bool) []byte {
	if len(raw) == 0 {
		return raw
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return raw
	}
	oauthRaw, ok := cfg["oauth_providers"]
	if !ok {
		return raw
	}
	oauth, ok := oauthRaw.(map[string]interface{})
	if !ok {
		return raw
	}
	for providerName, providerRaw := range oauth {
		provider, ok := providerRaw.(map[string]interface{})
		if !ok {
			continue
		}
		provider["secret_set"] = hasSecret(providerName)
		delete(provider, "client_secret")
		oauth[providerName] = provider
	}
	cfg["oauth_providers"] = oauth
	annotated, err := json.Marshal(cfg)
	if err != nil {
		return raw
	}
	return annotated
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
