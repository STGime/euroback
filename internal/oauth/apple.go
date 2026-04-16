package oauth

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AppleProvider implements Sign in with Apple.
//
// Key differences from standard OAuth providers:
//   - The client_secret is a short-lived JWT generated from a private key (ES256).
//   - User info comes from the id_token JWT (no userinfo endpoint).
//   - Apple uses response_mode=form_post (callback is POST, not GET).
//   - User name is only sent on the first authorization.
type AppleProvider struct{}

func (a *AppleProvider) Name() string { return "apple" }

func (a *AppleProvider) AuthURL(cfg AuthURLConfig) string {
	params := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURL},
		"response_type": {"code"},
		"scope":         {"name email"},
		"state":         {cfg.State},
		"response_mode": {"form_post"},
	}
	return "https://appleid.apple.com/auth/authorize?" + params.Encode()
}

// ExchangeCode exchanges an Apple authorization code for user info.
// cfg.ClientSecret must be the PEM-encoded ES256 private key from Apple.
// cfg.TeamID and cfg.KeyID must be set.
func (a *AppleProvider) ExchangeCode(ctx context.Context, cfg ExchangeConfig) (*UserInfo, error) {
	if cfg.TeamID == "" || cfg.KeyID == "" {
		return nil, fmt.Errorf("apple oauth: team_id and key_id are required")
	}

	// Generate the JWT client_secret from the PEM private key.
	clientSecret, err := generateAppleClientSecret(cfg.ClientID, cfg.TeamID, cfg.KeyID, cfg.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("apple oauth: generate client_secret: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// Exchange authorization code for tokens.
	tokenResp, err := client.PostForm("https://appleid.apple.com/auth/token", url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {clientSecret},
		"code":          {cfg.Code},
		"redirect_uri":  {cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		slog.Error("apple oauth: token exchange failed", "error", err)
		return nil, fmt.Errorf("apple token exchange: %w", err)
	}
	defer tokenResp.Body.Close()

	var tokenData struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return nil, fmt.Errorf("apple token parse: %w", err)
	}
	if tokenData.Error != "" {
		slog.Error("apple oauth: token error", "error", tokenData.Error)
		return nil, fmt.Errorf("apple token error: %s", tokenData.Error)
	}
	if tokenData.IDToken == "" {
		return nil, fmt.Errorf("apple oauth: no id_token in response")
	}

	// Parse the id_token to extract user info.
	// We don't verify the signature here because the token came directly from
	// Apple over TLS in the token exchange — it's trustworthy.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(tokenData.IDToken, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("apple id_token parse: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("apple oauth: invalid id_token claims")
	}

	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)

	if sub == "" {
		return nil, fmt.Errorf("apple oauth: no sub in id_token")
	}
	if email == "" {
		return nil, fmt.Errorf("apple oauth: no email in id_token")
	}

	return &UserInfo{
		Email:      email,
		Name:       "", // Apple only sends name on first auth; handled by caller
		AvatarURL:  "",
		ProviderID: sub,
	}, nil
}

// generateAppleClientSecret creates a short-lived JWT signed with ES256
// to use as the client_secret in Apple's token exchange.
func generateAppleClientSecret(clientID, teamID, keyID, pemKey string) (string, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iss": teamID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
		"aud": "https://appleid.apple.com",
		"sub": clientID,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = keyID

	return token.SignedString(key)
}
