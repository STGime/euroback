package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testSecretA = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	testSecretB = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

func withAlg(t *testing.T, alg string) {
	t.Helper()
	prev := EndUserTokenAlg()
	if err := SetEndUserTokenAlg(alg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetEndUserTokenAlg(prev) })
}

func testClaims(exp time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"sub":        "user-1",
		"email":      "a@example.com",
		"project_id": "proj-a",
		"type":       "enduser",
		"iss":        "eurobase",
		"iat":        time.Now().Unix(),
		"exp":        exp.Unix(),
	}
}

// The derivation must never change: customers cache the JWKS and tokens in
// flight are signed with this key. If this test fails, the derivation
// changed — revert it.
func TestEndUserSigningKey_PinnedVector(t *testing.T) {
	k, err := deriveEndUserKey(testSecretA)
	if err != nil {
		t.Fatal(err)
	}
	const wantKID = "LuN_gQ7dyo49Jg2h8wOeqOIg4FNsOAJGlvc5cFBTNE0"
	const wantX = "ijScYaqiG27m21lqXgpfWP6h42kshTrE7SzmGSeWaxo"
	if k.KID != wantKID || k.X != wantX {
		t.Fatalf("derivation changed: kid=%s x=%s", k.KID, k.X)
	}
}

func TestEndUserSigningKey_DeterministicPerProject(t *testing.T) {
	a1, err := deriveEndUserKey(testSecretA)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := deriveEndUserKey(testSecretA)
	b, _ := deriveEndUserKey(testSecretB)
	if a1.KID != a2.KID || a1.X != a2.X || a1.Y != a2.Y {
		t.Fatal("same secret must give the same key")
	}
	if a1.KID == b.KID || a1.X == b.X {
		t.Fatal("different secrets must give different keys")
	}
}

func TestSetEndUserTokenAlg(t *testing.T) {
	withAlg(t, "HS256")
	for _, ok := range []string{"", "HS256", "hs256", " ES256 ", "es256"} {
		if err := SetEndUserTokenAlg(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"RS256", "none", "ES384"} {
		if err := SetEndUserTokenAlg(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSignEndUserToken_HS256Default(t *testing.T) {
	withAlg(t, "HS256")
	tok, err := SignEndUserToken(testClaims(time.Now().Add(time.Hour)), testSecretA, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _ := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	if parsed.Method.Alg() != "HS256" {
		t.Fatalf("alg = %s", parsed.Method.Alg())
	}
	if _, ok := parsed.Claims.(jwt.MapClaims)["aud"]; ok {
		t.Fatal("HS256 tokens keep their claims unchanged (no aud)")
	}
	c, err := ValidateEndUserJWT(tok, testSecretA)
	if err != nil || c.UserID != "user-1" {
		t.Fatalf("validate: %v %+v", err, c)
	}
}

func TestSignEndUserToken_ES256(t *testing.T) {
	withAlg(t, "ES256")
	tok, err := SignEndUserToken(testClaims(time.Now().Add(time.Hour)), testSecretA, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _ := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	k, _ := EndUserKeyFor(testSecretA)
	if parsed.Method.Alg() != "ES256" || parsed.Header["kid"] != k.KID {
		t.Fatalf("header = %v", parsed.Header)
	}
	if parsed.Claims.(jwt.MapClaims)["aud"] != "proj-a" {
		t.Fatal("aud must be the project id")
	}
	c, err := ValidateEndUserJWT(tok, testSecretA)
	if err != nil || c.UserID != "user-1" || c.ProjectID != "proj-a" {
		t.Fatalf("validate: %v %+v", err, c)
	}

	if _, err := SignEndUserToken(testClaims(time.Now().Add(time.Hour)), testSecretA, ""); err == nil {
		t.Fatal("ES256 token without a project id (no aud) was signed")
	}

	// Another project's key must not verify it.
	if _, err := ValidateEndUserJWT(tok, testSecretB); err == nil {
		t.Fatal("token verified with another project's key")
	}

	// HS256 tokens issued before the switch keep working.
	withAlg(t, "HS256")
	old, _ := SignEndUserToken(testClaims(time.Now().Add(time.Hour)), testSecretA, "proj-a")
	withAlg(t, "ES256")
	if _, err := ValidateEndUserJWT(old, testSecretA); err != nil {
		t.Fatalf("HS256 token refused after the switch: %v", err)
	}
}

func TestValidateEndUserJWT_ES256Refusals(t *testing.T) {
	k, _ := EndUserKeyFor(testSecretA)

	sign := func(claims jwt.MapClaims, kid string, key *ecdsa.PrivateKey) string {
		tk := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		if kid != "" {
			tk.Header["kid"] = kid
		}
		s, err := tk.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	expired := sign(testClaims(time.Now().Add(-time.Minute)), k.KID, k.Private)
	if _, err := ValidateEndUserJWT(expired, testSecretA); err == nil {
		t.Error("expired ES256 token accepted")
	}

	wrongKID := sign(testClaims(time.Now().Add(time.Hour)), "someone-else", k.Private)
	if _, err := ValidateEndUserJWT(wrongKID, testSecretA); err == nil {
		t.Error("token with a foreign kid accepted")
	}

	numericKID := jwt.NewWithClaims(jwt.SigningMethodES256, testClaims(time.Now().Add(time.Hour)))
	numericKID.Header["kid"] = 123
	nk, _ := numericKID.SignedString(k.Private)
	if _, err := ValidateEndUserJWT(nk, testSecretA); err == nil {
		t.Error("token with a non-string kid accepted")
	}

	other, _ := EndUserKeyFor(testSecretB)
	forged := sign(testClaims(time.Now().Add(time.Hour)), k.KID, other.Private)
	if _, err := ValidateEndUserJWT(forged, testSecretA); err == nil {
		t.Error("token signed with another key accepted")
	}

	notEndUser := testClaims(time.Now().Add(time.Hour))
	notEndUser["type"] = "platform"
	if _, err := ValidateEndUserJWT(sign(notEndUser, k.KID, k.Private), testSecretA); err == nil {
		t.Error("non-enduser ES256 token accepted")
	}

	// Algorithm confusion: an HS256 token whose HMAC key is the public
	// key (what an attacker knows) must fail.
	pub, _ := k.Private.PublicKey.Bytes()
	confused, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, testClaims(time.Now().Add(time.Hour))).SignedString(pub)
	if _, err := ValidateEndUserJWT(confused, testSecretA); err == nil {
		t.Error("HS256 token keyed with the public key accepted")
	}

	// alg=none.
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, testClaims(time.Now().Add(time.Hour))).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := ValidateEndUserJWT(none, testSecretA); err == nil {
		t.Error("alg=none accepted")
	}

	// Other asymmetric algorithms are refused even with a valid-looking key.
	es384, _ := jwt.NewWithClaims(jwt.SigningMethodES384, testClaims(time.Now().Add(time.Hour))).SignedString(k.Private)
	if _, err := ValidateEndUserJWT(es384, testSecretA); err == nil {
		t.Error("ES384 accepted")
	}
}

func TestHandleEndUserJWKS(t *testing.T) {
	// Without a project (not on a project subdomain): 404.
	rec := httptest.NewRecorder()
	HandleEndUserJWKS(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/.well-known/jwks.json", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no project: status %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/.well-known/jwks.json", nil)
	req = req.WithContext(ContextWithProject(req.Context(), &ProjectContext{ProjectID: "proj-a", JWTSecret: testSecretA}))
	rec = httptest.NewRecorder()
	HandleEndUserJWKS(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Error("JWKS should be cacheable")
	}
	body := rec.Body.String()
	if strings.Contains(body, `"d"`) || strings.Contains(body, testSecretA) {
		t.Fatal("JWKS leaks private material")
	}
	var doc struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc.Keys) != 1 {
		t.Fatalf("jwks = %s", body)
	}
	jwk := doc.Keys[0]
	if jwk["kty"] != "EC" || jwk["crv"] != "P-256" || jwk["alg"] != "ES256" || jwk["use"] != "sig" {
		t.Fatalf("jwk = %v", jwk)
	}

	// A verifier that knows only the JWKS can verify an ES256 token.
	x, _ := base64.RawURLEncoding.DecodeString(jwk["x"])
	y, _ := base64.RawURLEncoding.DecodeString(jwk["y"])
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	if err != nil {
		t.Fatal(err)
	}
	withAlg(t, "ES256")
	tok, _ := SignEndUserToken(testClaims(time.Now().Add(time.Hour)), testSecretA, "proj-a")
	parsed, err := jwt.Parse(tok, func(tk *jwt.Token) (interface{}, error) {
		if tk.Header["kid"] != jwk["kid"] {
			t.Errorf("kid mismatch")
		}
		return pub, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer("eurobase"), jwt.WithAudience("proj-a"))
	if err != nil || !parsed.Valid {
		t.Fatalf("JWKS-only verification failed: %v", err)
	}
}
