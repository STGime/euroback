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
	// Microsoft-specific (ignored by other providers). Empty, "common",
	// "organizations", or "consumers" enable multi-tenant login; a specific
	// Entra tenant GUID locks sign-in to one organisation.
	TenantID string `json:"tenant_id,omitempty"`
}

// AuthConfig holds the per-project authentication configuration.
type AuthConfig struct {
	Providers                map[string]ProviderConfig      `json:"providers"`
	OAuthProviders           map[string]OAuthProviderConfig `json:"oauth_providers,omitempty"`
	PasswordMinLength        int                            `json:"password_min_length"`
	RequireEmailConfirmation bool                           `json:"require_email_confirmation"`
	SessionDuration          string                         `json:"session_duration"`
	RedirectURLs             []string                       `json:"redirect_urls"`
	// CORSOrigins are browser origins permitted to call this project's
	// API endpoints. Format: scheme + host + optional :port, no path.
	// Examples: "http://localhost:3000", "https://app.example.com".
	// The platform's own origins (eurobase.app, *.eurobase.app, plus any
	// gateway-configured ALLOWED_ORIGINS) are always allowed regardless
	// of this setting; this list is purely additive for tenant-owned
	// browser apps. Empty list = same as default (platform origins only).
	CORSOrigins []string `json:"cors_origins,omitempty"`

	// RateLimits is the per-project override block for the Supabase-style
	// Rate Limits page (#224). Pointer (and `omitempty`) so an absent
	// sub-object reads as "use defaults" rather than "all zeros". Each
	// knob inside is also optional — zero means "use the default for
	// this knob only", so a project can override only the values it
	// cares about. See DefaultRateLimits and EffectiveRateLimits below.
	RateLimits *RateLimits `json:"rate_limits,omitempty"`
}

// RateLimits is the per-project overrides surface for the Rate Limits page
// (#224 umbrella). Every field is a "zero means default" override — see
// EffectiveRateLimits for the merge.
//
// The platform also enforces per-identifier anti-brute-force counters
// (signin failures, forgot-password, magic-link, resend-verify, phone
// OTP) at platform-wide defaults; those are NOT exposed here because the
// Supabase Rate Limits page deliberately doesn't surface them — they're
// security floors, not knobs.
type RateLimits struct {
	// SignupSigninPer5MinPerIP gates the per-IP request volume on
	// /v1/auth/signup and /v1/auth/signin together. The per-email
	// signin-failure counter is a separate axis (anti-brute-force, not
	// surfaced) and continues to apply.
	SignupSigninPer5MinPerIP int `json:"signup_signin_per_5min_per_ip,omitempty"`

	// TokenRefreshPer5MinPerIP gates POST /v1/auth/refresh per IP.
	// Wired in #226.
	TokenRefreshPer5MinPerIP int `json:"token_refresh_per_5min_per_ip,omitempty"`

	// TokenVerificationPer5MinPerIP gates the OTP verify + magic-link
	// verify endpoints per IP. Wired in #226.
	TokenVerificationPer5MinPerIP int `json:"token_verification_per_5min_per_ip,omitempty"`

	// EmailsPerHour caps platform-side outbound emails per project per
	// rolling hour, regardless of trigger (verification, password reset,
	// magic link, raw). Wired in #227.
	EmailsPerHour int `json:"emails_per_hour,omitempty"`

	// SMSPerHour caps platform-side outbound SMS per project per
	// rolling hour. Wired in #227.
	SMSPerHour int `json:"sms_per_hour,omitempty"`

	// TrustProxy controls whether the per-project rate limiter keys off
	// the leftmost X-Forwarded-For entry (true) or the TCP peer (false).
	// Enforced by ratelimit.ClientIPForProject (#228).
	//
	// *bool, not bool, so we can tell "user explicitly set false" apart
	// from "field absent → use platform default". With a plain bool,
	// any project that ever saved a RateLimits sub-object without a
	// trust_proxy field would lock in `false` regardless of what the
	// platform default later said. The pointer keeps the override
	// honest — nil means "I haven't decided; pick the platform's
	// answer". EffectiveRateLimits guarantees the pointer is non-nil
	// in its return value, so callers can dereference safely.
	//
	// ── The two-direction trade-off ──
	//
	// The choice between `true` and `false` here trades one failure
	// mode for the opposite one — the right answer depends on the
	// deployment, and "safe by default" depends on which side you're
	// most worried about.
	//
	// TrustProxy = true (key on leftmost XFF):
	//   * Correct only if **exactly one trusted hop authoritatively
	//     overwrites XFF with the real client IP**. nginx-ingress with
	//     `use-forwarded-headers: false` does exactly that.
	//   * Gives true per-end-user keying — the published
	//     "per-IP" knob means what the UI says.
	//   * Failure mode: if the chain APPENDS to XFF (typical with
	//     `use-forwarded-headers: true`, sometimes needed to recover
	//     the client IP through a load balancer), leftmost-XFF is
	//     client-controlled. An attacker rotating the header bypasses
	//     every per-IP gate.
	//
	// TrustProxy = false (key on TCP peer):
	//   * Safe under any XFF configuration — no infra assumption.
	//   * Failure mode: in deployments that pre-aggregate behind a
	//     controlled hop (every Eurobase prod request comes through
	//     one nginx pod), `r.RemoteAddr` is the same value for every
	//     request. The "per-IP" gate effectively becomes per-project
	//     total — a 9-person office team can't all sign up in the
	//     same hour.
	//
	// ── Why the Eurobase default is false (for now) ──
	//
	// The default-true correctness depends on the Scaleway LB ↔
	// nginx-ingress XFF behavior — none of which is declared in this
	// repo (the Ingress YAML only sets ssl-redirect and proxy-body-
	// size; there's no controller ConfigMap or LB-side proxy-protocol
	// annotation). Until that's verified empirically in prod, shipping
	// `true` as the default would lean on an assumption we haven't
	// confirmed: if a future operator (or a never-noticed current
	// config) flips `use-forwarded-headers: true`, every per-IP gate
	// becomes a header-rotation bypass without anything in code
	// changing.
	//
	// So we ship `false`, accept the per-project-total degradation,
	// and track the proper flip in a follow-up: verify what XFF the
	// gateway actually receives, document the required ingress + LB
	// config as a hard precondition, then re-default to true. The
	// long-term hardening is a trusted-hop-count / known-proxy-CIDR
	// strategy that's robust to both extra hops and header forgery;
	// the same follow-up covers that.
	//
	// Projects that know their deployment trusts XFF can opt in today
	// by setting `"trust_proxy": true` in auth_config; the console UI
	// in #229 will surface this choice with the same trade-off
	// written out.
	TrustProxy *bool `json:"trust_proxy,omitempty"`
}

// DefaultRateLimits returns the platform-wide defaults applied when a
// project hasn't overridden a knob.
//
// Most numbers mirror Supabase's published defaults. Two deliberate
// divergences:
//
//   * SignupSigninPer5MinPerIP = 8 (not 30): held at the interim
//     ~96/h floor while EmailsPerHour enforcement is parked behind
//     the BYO-SMTP feature in #235. Bumps to 30 when that lands.
//
// TrustProxy ships as false (Supabase parity, safe-by-default). In
// Eurobase's deployment a true default would key off the nginx-ingress
// XFF rewrite — and *if* `use-forwarded-headers: true` is set anywhere
// in the Scaleway-LB → nginx chain, leftmost XFF becomes
// client-controlled and every per-IP gate becomes header-rotation-
// bypassable. A separate follow-up issue tracks empirically verifying
// the chain's XFF behavior in prod; once confirmed safe, the default
// can flip via the console (or this constant). Project owners who know
// their deployment trusts XFF can opt in today — see the TrustProxy
// field comment for the precondition.
//
// SMSPerHour IS enforced — projects bring their own GatewayAPI budget
// via configuration so a per-project cap is a real safeguard, not a
// self-DoS. EmailsPerHour exists for UI surfacing but has no code
// path consuming it today; #235 unblocks email enforcement.
func DefaultRateLimits() RateLimits {
	falseVal := false
	return RateLimits{
		SignupSigninPer5MinPerIP:      8,
		TokenRefreshPer5MinPerIP:      150,
		TokenVerificationPer5MinPerIP: 30,
		EmailsPerHour:                 2,
		SMSPerHour:                    30,
		TrustProxy:                    &falseVal,
	}
}

// EffectiveRateLimits returns the merged knobs: each zero-valued override
// in the project's stored config is filled from DefaultRateLimits. Safe to
// call on a config whose RateLimits field is nil.
//
// The returned value is guaranteed to have all int fields non-zero AND
// TrustProxy non-nil, so callers can read every field without nil checks.
// (TrustProxy is *bool in the storage shape so we can distinguish
// "explicit false" from "field absent"; the merge below collapses that
// distinction so the consumer never has to.)
func (c *AuthConfig) EffectiveRateLimits() RateLimits {
	defaults := DefaultRateLimits()
	if c == nil || c.RateLimits == nil {
		return defaults
	}
	out := *c.RateLimits
	if out.SignupSigninPer5MinPerIP == 0 {
		out.SignupSigninPer5MinPerIP = defaults.SignupSigninPer5MinPerIP
	}
	if out.TokenRefreshPer5MinPerIP == 0 {
		out.TokenRefreshPer5MinPerIP = defaults.TokenRefreshPer5MinPerIP
	}
	if out.TokenVerificationPer5MinPerIP == 0 {
		out.TokenVerificationPer5MinPerIP = defaults.TokenVerificationPer5MinPerIP
	}
	if out.EmailsPerHour == 0 {
		out.EmailsPerHour = defaults.EmailsPerHour
	}
	if out.SMSPerHour == 0 {
		out.SMSPerHour = defaults.SMSPerHour
	}
	if out.TrustProxy == nil {
		out.TrustProxy = defaults.TrustProxy
	}
	return out
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
		// Match RedirectURLs (issue #198): the scaffold targets a
		// localhost:3000 browser app, and an origin trusted to receive
		// OAuth redirects must also be CORS-allowed for API calls or
		// every fresh browser app hits an opaque CORS wall. Remote
		// sites cannot forge a localhost Origin header, so keeping
		// this entry on production projects is not an exposure.
		CORSOrigins: []string{"http://localhost:3000"},
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

	for _, raw := range c.CORSOrigins {
		// CORS origin format: scheme://host[:port], no path. Browsers
		// send the Origin header in this exact shape; mismatches don't
		// match.
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid cors_origin: %s (use scheme://host[:port])", raw)
		}
		if u.Path != "" && u.Path != "/" {
			return fmt.Errorf("cors_origin must not include a path: %s", raw)
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("cors_origin must not include query or fragment: %s", raw)
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

// IsPhoneAuthEnabled returns whether the phone provider is enabled.
func (c *AuthConfig) IsPhoneAuthEnabled() bool {
	p, ok := c.Providers["phone"]
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

// IsCORSOriginAllowed reports whether the given browser Origin header
// matches one of the project's configured cors_origins entries. Match
// is exact on scheme+host+port — the spec requires browsers to send
// the Origin in canonical form, so substring matches and trailing
// slashes are not accepted.
func (c *AuthConfig) IsCORSOriginAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	for _, allowed := range c.CORSOrigins {
		// Trim a trailing slash someone may have stored — purely a
		// quality-of-life fix, the Validate() above also strips it on
		// write. Browsers themselves never send a trailing slash.
		if strings.TrimRight(allowed, "/") == origin {
			return true
		}
	}
	return false
}

// IsRedirectURLAllowed checks whether the given URL is in the allowed redirect list.
//
// Path matching requires a segment boundary — closes #48. Previously a
// substring HasPrefix let `https://app.example.com/cb-evil` match an
// allowed entry of `https://app.example.com/cb`, which (combined with a
// foothold on a same-host path) would let an attacker intercept the OAuth
// authorization code.
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
		if a.Scheme != u.Scheme || a.Host != u.Host {
			continue
		}
		// Empty/root allowed-path → any path on the host is fine.
		if a.Path == "" || a.Path == "/" {
			return true
		}
		allowedURL := a.Scheme + "://" + a.Host + a.Path
		// Exact path match, OR the candidate path is a deeper segment
		// (must start with allowed + "/" to enforce a boundary).
		// `u.Path` parsed off the candidate already strips query and
		// fragment, so segment-boundary on `/` is the only concern.
		if candidate == allowedURL || strings.HasPrefix(candidate, allowedURL+"/") {
			return true
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
