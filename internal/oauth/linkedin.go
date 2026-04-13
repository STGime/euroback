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

// LinkedInProvider implements OAuth for LinkedIn using OpenID Connect.
type LinkedInProvider struct{}

func (l *LinkedInProvider) Name() string { return "linkedin" }

func (l *LinkedInProvider) AuthURL(clientID, redirectURL, state string) string {
	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURL},
		"response_type": {"code"},
		"scope":         {"openid profile email"},
		"state":         {state},
	}
	return "https://www.linkedin.com/oauth/v2/authorization?" + params.Encode()
}

func (l *LinkedInProvider) ExchangeCode(ctx context.Context, cfg ExchangeConfig) (*UserInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	// Exchange authorization code for access token.
	tokenResp, err := client.PostForm("https://www.linkedin.com/oauth/v2/accessToken", url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {cfg.Code},
		"redirect_uri":  {cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		slog.Error("linkedin oauth: token exchange failed", "error", err)
		return nil, fmt.Errorf("linkedin token exchange: %w", err)
	}
	defer tokenResp.Body.Close()

	var tokenData struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return nil, fmt.Errorf("linkedin token parse: %w", err)
	}
	if tokenData.Error != "" {
		slog.Error("linkedin oauth: token error", "error", tokenData.Error, "description", tokenData.ErrorDesc)
		return nil, fmt.Errorf("linkedin token error: %s", tokenData.ErrorDesc)
	}

	// Fetch user info via OpenID Connect userinfo endpoint.
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.linkedin.com/v2/userinfo", nil)
	if err != nil {
		return nil, fmt.Errorf("linkedin userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)

	userResp, err := client.Do(req)
	if err != nil {
		slog.Error("linkedin oauth: userinfo fetch failed", "error", err)
		return nil, fmt.Errorf("linkedin userinfo fetch: %w", err)
	}
	defer userResp.Body.Close()

	var userData struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&userData); err != nil {
		return nil, fmt.Errorf("linkedin userinfo parse: %w", err)
	}
	if userData.Email == "" {
		return nil, fmt.Errorf("linkedin oauth: no email returned")
	}

	return &UserInfo{
		Email:      userData.Email,
		Name:       userData.Name,
		AvatarURL:  userData.Picture,
		ProviderID: userData.Sub,
	}, nil
}
