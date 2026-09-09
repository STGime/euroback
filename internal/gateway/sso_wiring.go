package gateway

// Small wiring helpers for the Team-tier SSO routes. Kept in a
// separate file to avoid ballooning router.go's already-huge
// import list + function signature.

import (
	"context"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/tenant"
)

// SSOWiring bundles the pieces gateway/main.go constructs at
// startup and hands to NewRouter. Keeps the SSO surface togglable
// (all-nil = feature disabled + routes return 503).
type SSOWiring struct {
	// Orgs is the CRUD + config service. nil = SSO surface off.
	Orgs *tenant.OrgsService
	// SSOHandler runs the two OIDC endpoints. nil = SSO login off.
	SSOHandler *auth.SSOHandler
}

// Enabled reports whether the SSO wiring is fully populated.
func (w SSOWiring) Enabled() bool {
	return w.Orgs != nil && w.SSOHandler != nil
}

// ssoDisabledHandler returns 503 when the SSO surface is
// registered-but-off (missing PLATFORM_ENCRYPTION_KEY in the env,
// typical for local dev). Callers hitting the endpoint get a
// clear signal instead of a confusing 404.
func ssoDisabledHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"SSO is not configured on this deployment"}`))
}

// orgsSSOAdapter bridges *tenant.OrgsService (concrete) to the
// narrow auth.OrgsForSSO interface, translating tenant.OIDCConfig
// into auth.OIDCConfigForSSO so the auth package doesn't have to
// import tenant.
type orgsSSOAdapter struct {
	svc *tenant.OrgsService
}

// NewOrgsSSOAdapter builds the adapter. Used by main.go when
// constructing the SSO handler.
func NewOrgsSSOAdapter(svc *tenant.OrgsService) auth.OrgsForSSO {
	return &orgsSSOAdapter{svc: svc}
}

func (a *orgsSSOAdapter) FindOrgForSSOMember(ctx context.Context, email string) (string, error) {
	return a.svc.FindOrgForSSOMember(ctx, email)
}

func (a *orgsSSOAdapter) GetOIDCConfigForOrg(ctx context.Context, orgID string) (*auth.OIDCConfigForSSO, error) {
	cfg, err := a.svc.GetOIDCConfigForOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return &auth.OIDCConfigForSSO{
		Provider:     cfg.Provider,
		Issuer:       cfg.Issuer,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
	}, nil
}

func (a *orgsSSOAdapter) EnsureMemberFromSSO(ctx context.Context, orgID, platformUserID string) error {
	return a.svc.EnsureMemberFromSSO(ctx, orgID, platformUserID)
}
