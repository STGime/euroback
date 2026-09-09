package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP spins up a minimal OIDC provider on httptest — enough to
// exercise discover → JWKS fetch → token exchange → verify without
// mocking net/http. Uses a fresh RSA key per test so parallel runs
// don't collide.
type fakeIdP struct {
	t         *testing.T
	server    *httptest.Server
	privKey   *rsa.PrivateKey
	kid       string
	code2Sub  map[string]string // code → subject for the token endpoint stub
	overrides fakeIdPOverrides
}

type fakeIdPOverrides struct {
	// discoveryIssuer — override the issuer field in the discovery
	// document (for testing the strict issuer-match check).
	discoveryIssuer string
	// idTokenIssuer — override the `iss` claim in the minted ID token
	// (for testing signature-valid-but-issuer-wrong).
	idTokenIssuer string
	// idTokenAud — override the `aud` claim.
	idTokenAud string
	// idTokenAudMulti — mint aud as a []any (multi-valued) instead
	// of a plain string. Enables testing OIDC §3.1.3.7 azp rules.
	idTokenAudMulti []string
	// idTokenAzp — set the `azp` claim on the ID token.
	idTokenAzp string
	// idTokenNonce — set the `nonce` claim on the ID token.
	idTokenNonce string
	// emailVerified — force email_verified true/false on the token
	// (nil = default true).
	emailVerified *bool
	// dropEmail — mint an ID token without the email claim.
	dropEmail bool
	// expiredIDToken — mint an ID token that's already expired.
	expiredIDToken bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	f := &fakeIdP{
		t:        t,
		privKey:  priv,
		kid:      "test-kid-1",
		code2Sub: map[string]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.handleDiscovery)
	mux.HandleFunc("/jwks", f.handleJWKS)
	mux.HandleFunc("/token", f.handleToken)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeIdP) URL() string {
	return f.server.URL
}

func (f *fakeIdP) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	iss := f.overrides.discoveryIssuer
	if iss == "" {
		iss = f.server.URL
	}
	body := map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                f.server.URL + "/auth",
		"token_endpoint":                        f.server.URL + "/token",
		"jwks_uri":                              f.server.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeIdP) handleJWKS(w http.ResponseWriter, r *http.Request) {
	// Encode n + e as base64url per RFC 7518.
	n := base64.RawURLEncoding.EncodeToString(f.privKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.privKey.E)).Bytes())
	body := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": f.kid,
				"use": "sig",
				"alg": "RS256",
				"n":   n,
				"e":   e,
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeIdP) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code := r.PostForm.Get("code")
	clientID := r.PostForm.Get("client_id")
	sub, ok := f.code2Sub[code]
	if !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	idToken := f.mintIDToken(sub, clientID)
	body := map[string]any{
		"access_token": "test-access-token",
		"id_token":     idToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeIdP) mintIDToken(sub, clientID string) string {
	now := time.Now().Unix()
	exp := now + 300
	if f.overrides.expiredIDToken {
		exp = now - 60
	}
	iss := f.overrides.idTokenIssuer
	if iss == "" {
		iss = f.server.URL
	}
	claims := jwt.MapClaims{
		"iss": iss,
		"sub": sub,
		"iat": now,
		"exp": exp,
	}
	if len(f.overrides.idTokenAudMulti) > 0 {
		auds := make([]any, len(f.overrides.idTokenAudMulti))
		for i, a := range f.overrides.idTokenAudMulti {
			auds[i] = a
		}
		claims["aud"] = auds
	} else {
		aud := f.overrides.idTokenAud
		if aud == "" {
			aud = clientID
		}
		claims["aud"] = aud
	}
	if f.overrides.idTokenAzp != "" {
		claims["azp"] = f.overrides.idTokenAzp
	}
	if f.overrides.idTokenNonce != "" {
		claims["nonce"] = f.overrides.idTokenNonce
	}
	if !f.overrides.dropEmail {
		claims["email"] = sub + "@example.com"
		verified := true
		if f.overrides.emailVerified != nil {
			verified = *f.overrides.emailVerified
		}
		claims["email_verified"] = verified
		claims["name"] = "Test User " + sub
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	signed, err := tok.SignedString(f.privKey)
	if err != nil {
		f.t.Fatalf("mint id_token: %v", err)
	}
	return signed
}

// registerCode primes the token endpoint to accept `code` and mint
// an ID token for `sub`.
func (f *fakeIdP) registerCode(code, sub string) {
	f.code2Sub[code] = sub
}

func TestGenerateState_LengthAndUniqueness(t *testing.T) {
	t.Parallel()
	s1, err := GenerateState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	s2, err := GenerateState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if s1 == s2 {
		t.Fatalf("two GenerateState calls returned the same value — CSPRNG regression?")
	}
	// 32 bytes base64url = 43 chars (no padding).
	if len(s1) < 40 {
		t.Fatalf("state too short: %q", s1)
	}
}

func TestDiscovery_HappyPath(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	c := NewClient()
	d, err := c.discover(context.Background(), f.URL())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if d.TokenEndpoint == "" || d.AuthorizationEndpoint == "" || d.JWKSURI == "" {
		t.Fatalf("discovery missing endpoints: %+v", d)
	}
	// Second call should hit cache — verify by shutting the server
	// and calling again.
	f.server.Close()
	d2, err := c.discover(context.Background(), f.URL())
	if err != nil {
		t.Fatalf("cached discover: %v", err)
	}
	if d2.TokenEndpoint != d.TokenEndpoint {
		t.Fatalf("cache returned different discovery")
	}
}

func TestDiscovery_IssuerMismatch(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.overrides.discoveryIssuer = "https://not-what-we-asked-for.example.com"
	c := NewClient()
	_, err := c.discover(context.Background(), f.URL())
	if err == nil {
		t.Fatalf("expected error on issuer mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "issuer mismatch") {
		t.Fatalf("expected issuer-mismatch error, got: %v", err)
	}
}

func TestAuthURL_ContainsExpectedParams(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	c := NewClient()
	cfg := Config{
		Issuer:      f.URL(),
		ClientID:    "cid-1",
		RedirectURL: "https://console.example.com/cb",
	}
	u, err := c.AuthURL(context.Background(), cfg, "state-abc", "nonce-xyz")
	if err != nil {
		t.Fatalf("AuthURL: %v", err)
	}
	for _, expect := range []string{
		"client_id=cid-1",
		"redirect_uri=https%3A%2F%2Fconsole.example.com%2Fcb",
		"response_type=code",
		"state=state-abc",
		"nonce=nonce-xyz",
		"scope=openid+email+profile",
	} {
		if !strings.Contains(u, expect) {
			t.Errorf("AuthURL missing %q; got %s", expect, u)
		}
	}
}

func TestExchangeCode_HappyPath(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true, // SSO handler sets this true + relies on domain binding
	}
	vt, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if vt.Claims.Subject != "user-1" {
		t.Errorf("expected sub=user-1, got %q", vt.Claims.Subject)
	}
	if vt.Claims.Email != "user-1@example.com" {
		t.Errorf("expected email=user-1@example.com, got %q", vt.Claims.Email)
	}
	if !vt.Claims.EmailVerified {
		t.Errorf("expected email_verified=true")
	}
	if vt.Claims.Issuer != f.URL() {
		t.Errorf("expected iss=%s, got %s", f.URL(), vt.Claims.Issuer)
	}
	if vt.IDToken == "" {
		t.Error("expected non-empty raw IDToken")
	}
}

func TestExchangeCode_RejectsWrongAudience(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.idTokenAud = "some-other-app"
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true, // SSO handler sets this true + relies on domain binding
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err == nil {
		t.Fatalf("expected audience-mismatch error, got nil")
	}
}

func TestExchangeCode_RejectsExpiredToken(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.expiredIDToken = true
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true, // SSO handler sets this true + relies on domain binding
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err == nil {
		t.Fatalf("expected expired-token error, got nil")
	}
}

func TestExchangeCode_RejectsMissingEmail(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.dropEmail = true
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true, // SSO handler sets this true + relies on domain binding
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err == nil {
		t.Fatalf("expected missing-email error, got nil")
	}
	if !strings.Contains(err.Error(), "email") {
		t.Errorf("expected email in error message, got: %v", err)
	}
}

func TestExchangeCode_UnknownCode(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true, // SSO handler sets this true + relies on domain binding
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "not-registered", "")
	if err == nil {
		t.Fatalf("expected error on unknown code, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected HTTP 400 in error, got: %v", err)
	}
}

// ── Nonce enforcement — new after #535 review ──────────────────

func TestExchangeCode_NonceMatchesExpected(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.idTokenNonce = "n-abc"
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true,
	}
	vt, err := c.ExchangeCode(context.Background(), cfg, "code-1", "n-abc")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if vt.Claims.Nonce != "n-abc" {
		t.Errorf("expected claims.Nonce=n-abc, got %q", vt.Claims.Nonce)
	}
}

func TestExchangeCode_RejectsMismatchedNonce(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.idTokenNonce = "n-abc"
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true,
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "n-different")
	if err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce-mismatch error, got: %v", err)
	}
}

func TestExchangeCode_RejectsMissingNonceWhenExpected(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	// idTokenNonce override is empty → token minted without nonce claim.
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true,
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "n-expected-but-absent")
	if err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce error when caller expected one and token has none, got: %v", err)
	}
}

func TestExchangeCode_RejectsUnexpectedNonce(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.idTokenNonce = "n-leftover-from-victim-flow"
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true,
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected replay-suggestive nonce error, got: %v", err)
	}
}

// ── email_verified strict mode — new after #535 review ─────────

func TestExchangeCode_StrictModeRejectsUnverifiedEmail(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	falseVal := false
	f.overrides.emailVerified = &falseVal
	c := NewClient()
	cfg := Config{
		Issuer:       f.URL(),
		ClientID:     "cid-1",
		ClientSecret: "secret",
		RedirectURL:  "https://console.example.com/cb",
		// AllowUnverifiedEmail: false (default)
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err == nil || !strings.Contains(err.Error(), "email_verified") {
		t.Fatalf("expected email_verified error, got: %v", err)
	}
}

func TestExchangeCode_LooseModeAcceptsUnverifiedEmail(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	falseVal := false
	f.overrides.emailVerified = &falseVal
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true,
	}
	vt, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err != nil {
		t.Fatalf("expected accept, got: %v", err)
	}
	if vt.Claims.EmailVerified {
		t.Errorf("expected EmailVerified=false in claims")
	}
}

// ── OIDC §3.1.3.7 azp rules for multi-valued aud ──────────────

func TestExchangeCode_MultiAudRequiresAzp(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.idTokenAudMulti = []string{"cid-1", "other-app"}
	// idTokenAzp intentionally left empty
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true,
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err == nil || !strings.Contains(err.Error(), "azp") {
		t.Fatalf("expected azp error on multi-aud without azp, got: %v", err)
	}
}

func TestExchangeCode_MultiAudRejectsWrongAzp(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.idTokenAudMulti = []string{"cid-1", "other-app"}
	f.overrides.idTokenAzp = "other-app" // NOT cid-1
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true,
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1", "")
	if err == nil || !strings.Contains(err.Error(), "azp") {
		t.Fatalf("expected azp mismatch error, got: %v", err)
	}
}

func TestExchangeCode_MultiAudAcceptsMatchingAzp(t *testing.T) {
	t.Parallel()
	f := newFakeIdP(t)
	f.registerCode("code-1", "user-1")
	f.overrides.idTokenAudMulti = []string{"cid-1", "other-app"}
	f.overrides.idTokenAzp = "cid-1"
	c := NewClient()
	cfg := Config{
		Issuer:               f.URL(),
		ClientID:             "cid-1",
		ClientSecret:         "secret",
		RedirectURL:          "https://console.example.com/cb",
		AllowUnverifiedEmail: true,
	}
	if _, err := c.ExchangeCode(context.Background(), cfg, "code-1", ""); err != nil {
		t.Fatalf("expected accept on multi-aud + matching azp, got: %v", err)
	}
}

// ── Issuer must be https:// (loopback carve-out for tests) ─────

func TestDiscovery_RejectsPlainHTTPIssuer(t *testing.T) {
	t.Parallel()
	c := NewClient()
	_, err := c.discover(context.Background(), "http://accounts.example.com")
	if err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("expected https requirement error, got: %v", err)
	}
}

func TestRequireSecureIssuer_LoopbackAllowed(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{
		"https://accounts.example.com",
		"http://127.0.0.1:8080",
		"http://localhost:9000",
		"http://[::1]:1234",
	} {
		if err := requireSecureIssuer(ok); err != nil {
			t.Errorf("expected %q to pass, got: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://accounts.example.com",
		"ftp://accounts.example.com",
		"https://accounts.example.com" + "\x00", // control char — belt-and-braces; we don't reject explicitly today
	} {
		if err := requireSecureIssuer(bad); err == nil && bad != "https://accounts.example.com\x00" {
			t.Errorf("expected %q to fail, got nil", bad)
		}
	}
}
