package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// GoogleProvider implements OAuth for Google.
type GoogleProvider struct{}

func (g *GoogleProvider) Name() string { return "google" }

func (g *GoogleProvider) AuthURL(clientID, redirectURL, state string) string {
	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURL},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
	}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode()
}

func (g *GoogleProvider) ExchangeCode(ctx context.Context, cfg ExchangeConfig) (*UserInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	// Exchange authorization code for access token.
	tokenResp, err := client.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {cfg.Code},
		"redirect_uri":  {cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		slog.Error("google oauth: token exchange failed", "error", err)
		return nil, fmt.Errorf("google token exchange: %w", err)
	}
	defer tokenResp.Body.Close()

	var tokenData struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return nil, fmt.Errorf("google token parse: %w", err)
	}
	if tokenData.Error != "" {
		slog.Error("google oauth: token error", "error", tokenData.Error, "description", tokenData.ErrorDesc)
		return nil, fmt.Errorf("google token error: %s", tokenData.ErrorDesc)
	}

	// Fetch user info.
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return nil, fmt.Errorf("google userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)

	userResp, err := client.Do(req)
	if err != nil {
		slog.Error("google oauth: userinfo fetch failed", "error", err)
		return nil, fmt.Errorf("google userinfo fetch: %w", err)
	}
	defer userResp.Body.Close()

	var userData struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&userData); err != nil {
		return nil, fmt.Errorf("google userinfo parse: %w", err)
	}
	if userData.Email == "" {
		return nil, fmt.Errorf("google oauth: no email returned")
	}

	return &UserInfo{
		Email:      userData.Email,
		Name:       userData.Name,
		AvatarURL:  userData.Picture,
		ProviderID: userData.ID,
	}, nil
}
