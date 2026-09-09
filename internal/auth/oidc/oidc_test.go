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
	aud := f.overrides.idTokenAud
	if aud == "" {
		aud = clientID
	}
	claims := jwt.MapClaims{
		"iss": iss,
		"aud": aud,
		"sub": sub,
		"iat": now,
		"exp": exp,
	}
	if !f.overrides.dropEmail {
		claims["email"] = sub + "@example.com"
		claims["email_verified"] = true
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
		Issuer:       f.URL(),
		ClientID:     "cid-1",
		ClientSecret: "secret",
		RedirectURL:  "https://console.example.com/cb",
	}
	vt, err := c.ExchangeCode(context.Background(), cfg, "code-1")
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
		Issuer:       f.URL(),
		ClientID:     "cid-1",
		ClientSecret: "secret",
		RedirectURL:  "https://console.example.com/cb",
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1")
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
		Issuer:       f.URL(),
		ClientID:     "cid-1",
		ClientSecret: "secret",
		RedirectURL:  "https://console.example.com/cb",
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1")
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
		Issuer:       f.URL(),
		ClientID:     "cid-1",
		ClientSecret: "secret",
		RedirectURL:  "https://console.example.com/cb",
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "code-1")
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
		Issuer:       f.URL(),
		ClientID:     "cid-1",
		ClientSecret: "secret",
		RedirectURL:  "https://console.example.com/cb",
	}
	_, err := c.ExchangeCode(context.Background(), cfg, "not-registered")
	if err == nil {
		t.Fatalf("expected error on unknown code, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected HTTP 400 in error, got: %v", err)
	}
}
