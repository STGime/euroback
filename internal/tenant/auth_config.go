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

// AuthConfig holds the per-project authentication configuration.
type AuthConfig struct {
	Providers                map[string]ProviderConfig `json:"providers"`
	PasswordMinLength        int                       `json:"password_min_length"`
	RequireEmailConfirmation bool                      `json:"require_email_confirmation"`
	SessionDuration          string                    `json:"session_duration"`
	RedirectURLs             []string                  `json:"redirect_urls"`
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
