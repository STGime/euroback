package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// MicrosoftProvider implements OAuth (OIDC) for Microsoft Entra ID (Azure AD)
// / Microsoft 365 personal accounts.
//
// Key differences from other OIDC providers:
//   - The authorization + token endpoints are parameterised by a tenant
//     identifier in the URL path. `common` accepts both work/school and
//     personal accounts; `organizations` restricts to work/school; a GUID
//     locks the login to a single Entra tenant.
//   - The user profile is delivered inside the id_token (no separate
//     userinfo call required for the standard claims).
//   - If the configured tenant is a specific GUID, we additionally enforce
//     that the id_token's `tid` claim matches. This defends against token
//     substitution from other tenants when the app is registered as
//     single-tenant.
type MicrosoftProvider struct{}

func (m *MicrosoftProvider) Name() string { return "microsoft" }

// resolveTenant returns the tenant segment to drop into the authorize / token
// endpoint URL. Defaults to "common" (multi-tenant + personal) when the
// caller hasn't configured one.
func resolveMicrosoftTenant(tenantID string) string {
	t := strings.TrimSpace(tenantID)
	if t == "" {
		return "common"
	}
	return t
}

func (m *MicrosoftProvider) AuthURL(cfg AuthURLConfig) string {
	params := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURL},
		"response_type": {"code"},
		// offline_access requests a refresh token; email + profile pull the
		// standard OIDC claims into the id_token. Microsoft Graph scopes are
		// intentionally omitted — we don't need Graph access for sign-in.
		"scope":        {"openid email profile offline_access"},
		"state":        {cfg.State},
		"response_mode": {"query"},
	}
	return fmt.Sprintf(
		"https://login.microsoftonline.com/%s/oauth2/v2.0/authorize?%s",
		resolveMicrosoftTenant(cfg.TenantID),
		params.Encode(),
	)
}

// ExchangeCode swaps the authorization code for tokens and extracts the
// user profile from the id_token.
//
// The id_token is parsed without signature verification: Microsoft returned
// it to us directly over TLS in a POST we initiated ourselves, so the
// token's integrity is already guaranteed by the transport. This is the same
// stance the Apple provider takes and is explicitly sanctioned by the OIDC
// spec for the token-endpoint flow.
func (m *MicrosoftProvider) ExchangeCode(ctx context.Context, cfg ExchangeConfig) (*UserInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	tenant := resolveMicrosoftTenant(cfg.TenantID)
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant)

	form := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {cfg.Code},
		"redirect_uri":  {cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
		"scope":         {"openid email profile offline_access"},
	}

	tokenResp, err := client.PostForm(tokenURL, form)
	if err != nil {
		slog.Error("microsoft oauth: token exchange failed", "error", err)
		return nil, fmt.Errorf("microsoft token exchange: %w", err)
	}
	defer tokenResp.Body.Close()

	var tokenData struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return nil, fmt.Errorf("microsoft token parse: %w", err)
	}
	if tokenData.Error != "" {
		slog.Error("microsoft oauth: token error", "error", tokenData.Error, "description", tokenData.ErrorDesc)
		return nil, fmt.Errorf("microsoft token error: %s", tokenData.ErrorDesc)
	}
	if tokenData.IDToken == "" {
		return nil, fmt.Errorf("microsoft oauth: no id_token in response")
	}

	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(tokenData.IDToken, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("microsoft id_token parse: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("microsoft oauth: invalid id_token claims")
	}

	// Tenant-lock enforcement: if the caller configured a specific GUID
	// (not one of the multi-tenant aliases), reject tokens whose `tid`
	// doesn't match. Without this, a malicious app registered in a
	// different tenant could exchange a code against our token endpoint
	// using our client_id (in multi-tenant mode) and sneak a user in.
	if !isMicrosoftMultiTenantAlias(cfg.TenantID) {
		tid, _ := claims["tid"].(string)
		if tid == "" || !strings.EqualFold(tid, cfg.TenantID) {
			return nil, fmt.Errorf("microsoft oauth: id_token tenant %q does not match configured tenant %q", tid, cfg.TenantID)
		}
	}

	sub, _ := claims["sub"].(string)
	// The stable user identifier is the `oid` claim. `sub` is per-app and
	// rotates if the app registration changes; `oid` survives. Prefer
	// `oid`, fall back to `sub`.
	if oid, _ := claims["oid"].(string); oid != "" {
		sub = oid
	}
	if sub == "" {
		return nil, fmt.Errorf("microsoft oauth: no subject in id_token")
	}

	email, _ := claims["email"].(string)
	if email == "" {
		// Work accounts often lack the `email` claim; the UPN lives in
		// `preferred_username` instead and in practice matches the mailbox.
		email, _ = claims["preferred_username"].(string)
	}
	if email == "" {
		return nil, fmt.Errorf("microsoft oauth: no email or preferred_username in id_token")
	}

	name, _ := claims["name"].(string)

	return &UserInfo{
		Email:      email,
		Name:       name,
		AvatarURL:  "", // photo lives on Graph; skip in MVP to avoid an extra scope
		ProviderID: sub,
	}, nil
}

// isMicrosoftMultiTenantAlias reports whether the configured tenant value
// represents a multi-tenant entry point where the id_token's `tid` claim is
// expected to vary (and therefore should not be validated against a fixed
// value).
func isMicrosoftMultiTenantAlias(tenantID string) bool {
	switch strings.ToLower(strings.TrimSpace(tenantID)) {
	case "", "common", "organizations", "consumers":
		return true
	default:
		return false
	}
}
