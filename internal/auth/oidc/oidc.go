// Package oidc is a minimal OpenID Connect 1.0 client focused on
// what Team-tier platform SSO needs: discovery, authorization-code
// flow, and ID-token verification against JWKS.
//
// This is DELIBERATELY not a "kitchen sink" OIDC library — no PAR,
// no dynamic client registration, no back-channel logout, no
// userinfo endpoint fallback. Those are follow-ups when a customer
// needs them. The spec surface we support is enough for Google
// Workspace, Okta, Azure AD, Auth0, Keycloak, and dex — the IdPs
// enterprise customers actually use.
//
// The old internal/oauth/google.go is plain OAuth 2 (fetch access
// token, call /userinfo). This package is proper OIDC (verify the
// ID token cryptographically, trust its claims, avoid the second
// round-trip). Keeping them as separate packages so tenant end-user
// auth doesn't inherit the JWKS caching complexity, and platform
// SSO doesn't inherit the six-provider hardcoded dispatch table.
package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Config is one OIDC provider registration — matches the shape
// persisted in public.organizations.oidc_config (JSONB) minus the
// client_secret_ref indirection (this struct carries the resolved
// secret; the SSO handler fetches the plaintext from vault before
// calling us).
type Config struct {
	Issuer       string // https://accounts.google.com etc. — MUST match the ID token's `iss` claim
	ClientID     string
	ClientSecret string
	RedirectURL  string // https://console.eurobase.app/platform/auth/sso/callback
	// Scopes is optional; empty defaults to "openid email profile".
	Scopes []string

	// AllowUnverifiedEmail — when false (DEFAULT, safe posture),
	// ExchangeCode rejects an ID token whose `email_verified` claim
	// is either false or missing. When true, unverified emails are
	// accepted — but the caller MUST enforce domain-binding
	// (asserted email's domain == the org's DNS-TXT-verified
	// primary_email_domain from migration 000114) or a permissive
	// IdP asserting an arbitrary email becomes account-takeover.
	//
	// The Team-tier platform SSO handler sets this to true and
	// relies on the domain-binding path per the DNS-TXT protocol
	// documented on public.organizations.primary_email_domain (see
	// migration 000114 comment). Callers that DON'T do domain
	// binding must leave this false.
	AllowUnverifiedEmail bool
}

// Discovery is the subset of the OIDC provider config document
// (`/.well-known/openid-configuration`) this client cares about.
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ResponseTypes         []string `json:"response_types_supported"`
	IDTokenSignAlgs       []string `json:"id_token_signing_alg_values_supported"`

	fetchedAt time.Time
}

// Claims is the subset of ID-token claims relevant to platform-user
// provisioning. Extra claims (given_name, family_name, groups, hd)
// stay on the raw JWT for the caller to inspect via VerifiedToken.
type Claims struct {
	Subject       string `json:"sub"`
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Nonce         string `json:"nonce"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	ExpiresAt     int64  `json:"exp"`
	IssuedAt      int64  `json:"iat"`
}

// VerifiedToken is what ExchangeCode returns on success: the
// signature-verified ID token + the parsed subset of claims + the
// raw claims blob for callers that need extra fields.
type VerifiedToken struct {
	Claims    Claims
	RawClaims map[string]any // full claim set for provider-specific fields
	IDToken   string
}

// Client is the OIDC session runner. Not safe for concurrent
// construction; safe for concurrent use once built. Long-lived —
// caches discovery documents + JWKS per issuer.
type Client struct {
	httpClient *http.Client
	now        func() time.Time // injectable for tests

	// Cache keyed by issuer URL. Both entries are read-mostly after
	// warmup, so plain map + RWMutex is fine (a sync.Map would add
	// noise for no measurable win at our scale).
	mu        sync.RWMutex
	discCache map[string]*Discovery
	jwksCache map[string]keyfunc.Keyfunc
}

// requireSecureIssuer rejects issuer URLs that aren't https:// —
// closes the MITM-on-discovery bypass the #535 review flagged. An
// http:// discovery URL is a full compromise (attacker serves a
// forged document pointing at their own token + jwks endpoints).
// Loopback carve-out exists ONLY for httptest / unit-test servers
// (Go's httptest.NewServer binds to 127.0.0.1); admin config paths
// enforce https:// at the schema layer separately.
func requireSecureIssuer(issuer string) error {
	if strings.HasPrefix(issuer, "https://") {
		return nil
	}
	// Loopback carve-out for tests. httptest.NewServer emits
	// http://127.0.0.1:<port>/…; we tolerate that but nothing else.
	if strings.HasPrefix(issuer, "http://127.0.0.1:") ||
		strings.HasPrefix(issuer, "http://localhost:") ||
		strings.HasPrefix(issuer, "http://[::1]:") {
		return nil
	}
	return fmt.Errorf("issuer must be https:// (got %q) — an http:// discovery endpoint is trivially MITM-attackable", issuer)
}

// discoveryTTL — how long we trust a cached provider config before
// re-fetching. Providers rotate keys via the JWKS document
// (handled separately by keyfunc's own refresh), so this only
// covers endpoint changes, which are rare. Long TTL = cheap.
const discoveryTTL = 24 * time.Hour

// NewClient constructs an OIDC client with sensible HTTP timeouts.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		now:        time.Now,
		discCache:  map[string]*Discovery{},
		jwksCache:  map[string]keyfunc.Keyfunc{},
	}
}

// GenerateState returns a URL-safe random string suitable for the
// OIDC `state` parameter. 32 bytes of entropy = 256 bits, enough to
// resist online guessing forever.
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("state entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateNonce returns a random nonce for replay protection. Same
// entropy budget as GenerateState; kept as a separate helper so
// callers can be explicit about which value goes into which claim.
func GenerateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("nonce entropy: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// AuthURL builds the redirect URL for the RP-initiated login flow.
// state carries the caller's opaque round-trip data (e.g. org_id +
// signed timestamp); nonce goes into the ID token to detect
// replays. Both must be verified by the caller on callback.
func (c *Client) AuthURL(ctx context.Context, cfg Config, state, nonce string) (string, error) {
	d, err := c.discover(ctx, cfg.Issuer)
	if err != nil {
		return "", err
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "profile"}
	}
	q := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURL},
		"response_type": {"code"},
		"scope":         {strings.Join(scopes, " ")},
		"state":         {state},
		"nonce":         {nonce},
	}
	u, err := url.Parse(d.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse authorization_endpoint: %w", err)
	}
	// Preserve any static query the provider bakes into the endpoint
	// URL (rare but not illegal per RFC 6749) by merging rather than
	// overwriting u.RawQuery.
	existing := u.Query()
	for k, vs := range q {
		for _, v := range vs {
			existing.Set(k, v)
		}
	}
	u.RawQuery = existing.Encode()
	return u.String(), nil
}

// ExchangeCode runs the token endpoint call, cryptographically
// verifies the returned ID token against the discovered JWKS, and
// returns the extracted claims. The caller is responsible for
// verifying `state` (out-of-band from OIDC) — the nonce round-trip
// is checked here as long as the caller passes the same value they
// minted for AuthURL.
//
// expectedNonce MUST equal what was passed to AuthURL for this
// login flow. Empty expectedNonce = "no nonce was minted for this
// flow" — the token is rejected if it carries a nonce claim (an
// attacker injecting a replay would carry over the nonce from the
// original victim flow).
func (c *Client) ExchangeCode(ctx context.Context, cfg Config, code, expectedNonce string) (*VerifiedToken, error) {
	d, err := c.discover(ctx, cfg.Issuer)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURL},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Providers return a JSON body with {"error": "...",
		// "error_description": "..."} on 4xx. Preserve it — the
		// caller will need it to distinguish a stale-code error
		// (retryable UX) from a config-mismatch error (retry pointless).
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("token response parse: %w", err)
	}
	if tokenResp.IDToken == "" {
		return nil, errors.New("token endpoint response missing id_token — provider may not have granted openid scope")
	}

	return c.verifyIDToken(ctx, cfg, tokenResp.IDToken, expectedNonce)
}

// verifyIDToken parses the JWT, verifies the signature via the
// issuer's JWKS, and checks the standard claims (iss, aud, exp,
// nonce, email_verified per config).
func (c *Client) verifyIDToken(ctx context.Context, cfg Config, idToken, expectedNonce string) (*VerifiedToken, error) {
	kf, err := c.jwks(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("jwks fetch: %w", err)
	}

	parsed, err := jwt.Parse(idToken, kf.Keyfunc,
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithAudience(cfg.ClientID),
		jwt.WithValidMethods([]string{"RS256", "ES256", "RS384", "ES384"}),
		jwt.WithTimeFunc(c.now),
	)
	if err != nil {
		return nil, fmt.Errorf("id_token verify: %w", err)
	}
	if !parsed.Valid {
		return nil, errors.New("id_token failed verification (jwt marked invalid)")
	}

	rawClaims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("unexpected claims type: %T", parsed.Claims)
	}

	// Extract the well-known claims into our typed struct. Missing
	// email is fatal — SSO without an email is unusable for our
	// user-matching logic (platform_users is keyed by email).
	var claims Claims
	claims.Subject, _ = rawClaims["sub"].(string)
	claims.Issuer, _ = rawClaims["iss"].(string)
	// aud can be either a string or a []any per spec; we asked
	// jwt.WithAudience to enforce membership, so if we got here it
	// matched. Record it + additionally enforce OIDC §3.1.3.7 rule
	// 4/5: when aud is multi-valued, the token MUST carry an azp
	// (authorized party) claim equal to our client_id. Missing azp
	// on a multi-aud token is a spec violation and we reject.
	switch a := rawClaims["aud"].(type) {
	case string:
		claims.Audience = a
	case []any:
		if len(a) > 0 {
			claims.Audience, _ = a[0].(string)
		}
		if len(a) > 1 {
			azp, _ := rawClaims["azp"].(string)
			if azp == "" {
				return nil, errors.New("id_token has multi-valued aud but no azp claim (OIDC §3.1.3.7 violation)")
			}
			if azp != cfg.ClientID {
				return nil, fmt.Errorf("id_token azp=%q does not match client_id=%q", azp, cfg.ClientID)
			}
		}
	}
	claims.Nonce, _ = rawClaims["nonce"].(string)
	claims.Email, _ = rawClaims["email"].(string)
	claims.EmailVerified, _ = rawClaims["email_verified"].(bool)
	claims.Name, _ = rawClaims["name"].(string)
	if exp, ok := rawClaims["exp"].(float64); ok {
		claims.ExpiresAt = int64(exp)
	}
	if iat, ok := rawClaims["iat"].(float64); ok {
		claims.IssuedAt = int64(iat)
	}

	// Nonce enforcement — was previously "docs on the caller" and
	// silently forgettable. Reviewer's #535 finding: a token with
	// NO nonce claim was verifying fine. Now enforced here in every
	// case (fail-safe when the caller expected one; fail-safe when
	// the caller passed "" but the token has a nonce claim,
	// suggesting a replay).
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return nil, errors.New("id_token nonce does not match expected value — possible replay")
	}
	if expectedNonce == "" && claims.Nonce != "" {
		return nil, errors.New("id_token carries nonce but caller expected none — possible replay")
	}

	if claims.Email == "" {
		return nil, errors.New("id_token missing email claim — request `email` scope from the provider")
	}
	// email_verified: the safe default is to REJECT if the claim is
	// false or absent. Callers who intentionally rely on a
	// domain-binding layer (SSO handler + DNS-TXT-verified
	// primary_email_domain) can opt out via
	// Config.AllowUnverifiedEmail=true — that's the platform-SSO
	// path per migration 000114's DNS-verification protocol. Any
	// other caller (tenant-end-user auth, one-off scripts) gets the
	// strict behavior.
	if !cfg.AllowUnverifiedEmail && !claims.EmailVerified {
		return nil, errors.New("id_token email_verified is false or missing (set Config.AllowUnverifiedEmail=true if you're binding email domain to a verified org)")
	}

	return &VerifiedToken{
		Claims:    claims,
		RawClaims: rawClaims,
		IDToken:   idToken,
	}, nil
}

// discover returns the issuer's provider config, fetching + caching
// on cold path.
func (c *Client) discover(ctx context.Context, issuer string) (*Discovery, error) {
	if issuer == "" {
		return nil, errors.New("empty issuer")
	}
	issuer = strings.TrimRight(issuer, "/")
	if err := requireSecureIssuer(issuer); err != nil {
		return nil, err
	}

	c.mu.RLock()
	d, ok := c.discCache[issuer]
	c.mu.RUnlock()
	if ok && c.now().Sub(d.fetchedAt) < discoveryTTL {
		return d, nil
	}

	discoURL := issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build discovery request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("discovery %s returned %d: %s", discoURL, resp.StatusCode, string(body))
	}

	var fetched Discovery
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		return nil, fmt.Errorf("discovery parse: %w", err)
	}
	// RFC 8414 requires the discovered issuer to exactly match the
	// URL used to fetch — protects against a provider that thinks
	// it's example.com serving a document that claims issuer=
	// example.org.
	if strings.TrimRight(fetched.Issuer, "/") != issuer {
		return nil, fmt.Errorf("discovery issuer mismatch: expected %q, document claims %q", issuer, fetched.Issuer)
	}
	if fetched.TokenEndpoint == "" || fetched.AuthorizationEndpoint == "" || fetched.JWKSURI == "" {
		return nil, errors.New("discovery document missing required endpoints (token / authorization / jwks_uri)")
	}
	fetched.fetchedAt = c.now()

	c.mu.Lock()
	c.discCache[issuer] = &fetched
	c.mu.Unlock()

	return &fetched, nil
}

// jwks returns a keyfunc.Keyfunc for the issuer, building + caching
// on cold path. keyfunc handles its own JWKS refresh + kid-miss
// retry, so we only need to construct it once per issuer.
func (c *Client) jwks(ctx context.Context, issuer string) (keyfunc.Keyfunc, error) {
	issuer = strings.TrimRight(issuer, "/")

	c.mu.RLock()
	kf, ok := c.jwksCache[issuer]
	c.mu.RUnlock()
	if ok {
		return kf, nil
	}

	d, err := c.discover(ctx, issuer)
	if err != nil {
		return nil, err
	}

	// keyfunc's default options give us: JWKS auto-refresh every
	// hour, immediate refresh on kid miss, RS256 / ES256 support,
	// bounded concurrency. Same lib the tenant-side auth already
	// uses, so ops signal (metrics, error shape) is consistent.
	built, err := keyfunc.NewDefaultCtx(ctx, []string{d.JWKSURI})
	if err != nil {
		return nil, fmt.Errorf("build jwks for %s: %w", issuer, err)
	}

	c.mu.Lock()
	c.jwksCache[issuer] = built
	c.mu.Unlock()
	return built, nil
}
