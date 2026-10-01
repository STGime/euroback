package tenant

import (
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// A superadmin passes an org's sso_required only with a passkey session;
// everyone else only with an SSO session for that org.
func TestClaimsSatisfySSOFor(t *testing.T) {
	const org, other = "org-a", "org-b"
	cases := []struct {
		name     string
		claims   *auth.Claims
		required bool
		want     bool
	}{
		{"not required", &auth.Claims{LoginVia: auth.LoginViaPassword}, false, true},
		{"sso for this org", &auth.Claims{LoginVia: auth.LoginViaSSO, SsoOrgID: org}, true, true},
		{"sso for another org", &auth.Claims{LoginVia: auth.LoginViaSSO, SsoOrgID: other}, true, false},
		{"password", &auth.Claims{LoginVia: auth.LoginViaPassword}, true, false},
		{"passkey, not superadmin", &auth.Claims{LoginVia: auth.LoginViaPasskey}, true, false},
		{"superadmin, password", &auth.Claims{LoginVia: auth.LoginViaPassword, IsSuperadmin: true}, true, false},
		{"superadmin, passkey", &auth.Claims{LoginVia: auth.LoginViaPasskey, IsSuperadmin: true}, true, true},
		{"superadmin, token", &auth.Claims{LoginVia: auth.LoginViaPAT, IsSuperadmin: true}, true, false},
		{"no claims", nil, true, false},
	}
	for _, c := range cases {
		if got := ClaimsSatisfySSOFor(c.claims, org, c.required); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
