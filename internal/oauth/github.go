package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// GitHubProvider implements OAuth for GitHub.
type GitHubProvider struct{}

func (g *GitHubProvider) Name() string { return "github" }

func (g *GitHubProvider) AuthURL(clientID, redirectURL, state string) string {
	params := url.Values{
		"client_id":    {clientID},
		"redirect_uri": {redirectURL},
		"scope":        {"user:email"},
		"state":        {state},
	}
	return "https://github.com/login/oauth/authorize?" + params.Encode()
}

func (g *GitHubProvider) ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURL string) (*UserInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	// Exchange authorization code for access token.
	tokenBody, _ := json.Marshal(map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
	})
	tokenReq, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", bytes.NewReader(tokenBody))
	if err != nil {
		return nil, fmt.Errorf("github token request: %w", err)
	}
	tokenReq.Header.Set("Content-Type", "application/json")
	tokenReq.Header.Set("Accept", "application/json")

	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		slog.Error("github oauth: token exchange failed", "error", err)
		return nil, fmt.Errorf("github token exchange: %w", err)
	}
	defer tokenResp.Body.Close()

	var tokenData struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return nil, fmt.Errorf("github token parse: %w", err)
	}
	if tokenData.Error != "" {
		slog.Error("github oauth: token error", "error", tokenData.Error, "description", tokenData.ErrorDesc)
		return nil, fmt.Errorf("github token error: %s", tokenData.ErrorDesc)
	}

	// Fetch user profile.
	userReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("github user request: %w", err)
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
	userReq.Header.Set("User-Agent", "Eurobase-OAuth")

	userResp, err := client.Do(userReq)
	if err != nil {
		slog.Error("github oauth: user fetch failed", "error", err)
		return nil, fmt.Errorf("github user fetch: %w", err)
	}
	defer userResp.Body.Close()

	var userData struct {
		ID        int    `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
		Email     string `json:"email"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&userData); err != nil {
		return nil, fmt.Errorf("github user parse: %w", err)
	}

	email := userData.Email

	// If no public email, fetch primary email from /user/emails.
	if email == "" {
		emailReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/emails", nil)
		if err != nil {
			return nil, fmt.Errorf("github emails request: %w", err)
		}
		emailReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
		emailReq.Header.Set("User-Agent", "Eurobase-OAuth")

		emailResp, err := client.Do(emailReq)
		if err != nil {
			slog.Error("github oauth: emails fetch failed", "error", err)
			return nil, fmt.Errorf("github emails fetch: %w", err)
		}
		defer emailResp.Body.Close()

		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := json.NewDecoder(emailResp.Body).Decode(&emails); err != nil {
			return nil, fmt.Errorf("github emails parse: %w", err)
		}
		for _, e := range emails {
			if e.Primary && e.Verified {
				email = e.Email
				break
			}
		}
	}

	if email == "" {
		return nil, fmt.Errorf("github oauth: no verified email found")
	}

	name := userData.Name
	if name == "" {
		name = userData.Login
	}

	return &UserInfo{
		Email:      email,
		Name:       name,
		AvatarURL:  userData.AvatarURL,
		ProviderID: strconv.Itoa(userData.ID),
	}, nil
}
