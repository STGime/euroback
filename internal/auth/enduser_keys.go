package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/golang-jwt/jwt/v5"
)

// End-user access tokens can be signed with ES256 (#809), so a customer's
// server can verify them locally with the project's published public key
// (GET /v1/auth/.well-known/jwks.json) instead of calling GET /v1/auth/user.
//
// Each project's P-256 key pair is derived from its jwt_secret with HKDF,
// so there is nothing new to store, seal or provision: the private key
// exists only in gateway memory and is exactly as secret as jwt_secret,
// which can already mint HS256 tokens. The derivation must never change:
// a change would invalidate every ES256 token in flight and every JWKS a
// customer has cached (TestEndUserSigningKey_PinnedVector pins it).

const (
	endUserKeySalt = "eurobase/enduser-jwt"
	endUserKeyInfo = "es256-v1"
)

// EndUserSigningKey is a project's ES256 key pair and its key id.
type EndUserSigningKey struct {
	Private *ecdsa.PrivateKey
	KID     string // RFC 7638 JWK thumbprint (SHA-256, base64url)
	X, Y    string // base64url public point coordinates
}

var endUserKeyCache sync.Map // sha256(jwt_secret) hex → *EndUserSigningKey

// EndUserKeyFor returns the project's ES256 signing key, derived from its
// jwt_secret. Derivation is cached per secret.
func EndUserKeyFor(jwtSecret string) (*EndUserSigningKey, error) {
	if jwtSecret == "" {
		return nil, fmt.Errorf("project has no signing secret")
	}
	sum := sha256.Sum256([]byte(jwtSecret))
	cacheKey := hex.EncodeToString(sum[:])
	if k, ok := endUserKeyCache.Load(cacheKey); ok {
		return k.(*EndUserSigningKey), nil
	}
	k, err := deriveEndUserKey(jwtSecret)
	if err != nil {
		return nil, err
	}
	endUserKeyCache.Store(cacheKey, k)
	return k, nil
}

func deriveEndUserKey(jwtSecret string) (*EndUserSigningKey, error) {
	// A 32-byte HKDF output is a valid P-256 scalar unless it is zero or
	// ≥ the group order (probability ~2^-32); retry with a counter then.
	for i := 0; i < 16; i++ {
		info := endUserKeyInfo
		if i > 0 {
			info = fmt.Sprintf("%s|%d", endUserKeyInfo, i)
		}
		raw, err := hkdf.Key(sha256.New, []byte(jwtSecret), []byte(endUserKeySalt), info, 32)
		if err != nil {
			return nil, fmt.Errorf("derive signing key: %w", err)
		}
		priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
		if err != nil {
			continue
		}
		pub, err := priv.PublicKey.Bytes() // 0x04 || X || Y
		if err != nil || len(pub) != 65 {
			return nil, fmt.Errorf("encode public key: %v", err)
		}
		x := base64.RawURLEncoding.EncodeToString(pub[1:33])
		y := base64.RawURLEncoding.EncodeToString(pub[33:])
		// RFC 7638: required members in lexicographic order, no spaces.
		thumb := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"` + x + `","y":"` + y + `"}`))
		return &EndUserSigningKey{
			Private: priv,
			KID:     base64.RawURLEncoding.EncodeToString(thumb[:]),
			X:       x,
			Y:       y,
		}, nil
	}
	return nil, fmt.Errorf("derive signing key: no valid scalar")
}

// JWK is the public key in JWKS form.
func (k *EndUserSigningKey) JWK() map[string]string {
	return map[string]string{
		"kty": "EC",
		"crv": "P-256",
		"x":   k.X,
		"y":   k.Y,
		"kid": k.KID,
		"alg": "ES256",
		"use": "sig",
	}
}

// endUserSignES256 says whether new end-user access tokens are signed with
// ES256 (true) or HS256 (false). Verification accepts both either way, so
// the switch is rolled out in two deploys: first every gateway learns to
// verify ES256, then signing is switched on (ENDUSER_JWT_ALG=ES256).
var endUserSignES256 atomic.Bool

// SetEndUserTokenAlg sets the signing algorithm for new end-user access
// tokens: "HS256" (default) or "ES256".
func SetEndUserTokenAlg(alg string) error {
	switch strings.ToUpper(strings.TrimSpace(alg)) {
	case "", "HS256":
		endUserSignES256.Store(false)
	case "ES256":
		endUserSignES256.Store(true)
	default:
		return fmt.Errorf("ENDUSER_JWT_ALG must be HS256 or ES256, got %q", alg)
	}
	return nil
}

// EndUserTokenAlg reports the algorithm new end-user tokens are signed with.
func EndUserTokenAlg() string {
	if endUserSignES256.Load() {
		return "ES256"
	}
	return "HS256"
}

// SignEndUserToken signs end-user access-token claims for a project with
// the configured algorithm. ES256 tokens carry the key id in the header
// and the project id as the audience.
func SignEndUserToken(claims jwt.MapClaims, jwtSecret, projectID string) (string, error) {
	if !endUserSignES256.Load() {
		return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
	}
	if projectID == "" {
		// Verifiers are told to require aud = project id.
		return "", fmt.Errorf("sign ES256 token: missing project id")
	}
	k, err := EndUserKeyFor(jwtSecret)
	if err != nil {
		return "", err
	}
	c := make(jwt.MapClaims, len(claims)+1)
	for name, v := range claims {
		c[name] = v
	}
	c["aud"] = projectID
	t := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	t.Header["kid"] = k.KID
	return t.SignedString(k.Private)
}

// endUserVerifyKey is the jwt.Keyfunc for a project's end-user tokens:
// the jwt_secret for HS256, the derived public key for ES256.
func endUserVerifyKey(secret string) jwt.Keyfunc {
	return func(t *jwt.Token) (interface{}, error) {
		switch t.Method.(type) {
		case *jwt.SigningMethodHMAC:
			return []byte(secret), nil
		case *jwt.SigningMethodECDSA:
			k, err := EndUserKeyFor(secret)
			if err != nil {
				return nil, err
			}
			if raw, present := t.Header["kid"]; present {
				if kid, ok := raw.(string); !ok || kid != k.KID {
					return nil, fmt.Errorf("unknown key id")
				}
			}
			return &k.Private.PublicKey, nil
		default:
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
	}
}

// jwksResponse renders a project's JWKS document.
func jwksResponse(k *EndUserSigningKey) ([]byte, error) {
	return json.Marshal(map[string]any{"keys": []map[string]string{k.JWK()}})
}

// EndUserJWKSPath is where a project's end-user token JWKS is served.
const EndUserJWKSPath = "/v1/auth/.well-known/jwks.json"

// HandleEndUserJWKS serves GET /v1/auth/.well-known/jwks.json on a
// project's own URL (https://<slug>.<domain>): the public key a customer's
// server uses to verify ES256 end-user access tokens locally. Public and
// cacheable; the project comes from the subdomain (SubdomainMiddleware),
// not an API key, so standard JWKS clients can fetch it without headers.
// It is published whichever algorithm new tokens are signed with, so
// verifiers can be set up before ES256 signing is on.
func HandleEndUserJWKS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	pc, ok := ProjectFromContext(r.Context())
	if !ok || pc == nil || pc.JWTSecret == "" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"open this on your project URL: https://<project>.eurobase.app/v1/auth/.well-known/jwks.json"}`))
		return
	}
	k, err := EndUserKeyFor(pc.JWTSecret)
	if err != nil {
		slog.Error("jwks: derive key", "project_id", pc.ProjectID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
		return
	}
	body, err := jwksResponse(k)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}
