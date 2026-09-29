package tenant

import (
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// A project-scoped token (#702): only its project (another is "no
// access"), at most its own role and never more than the creator's
// current role; legacy tokens and console sessions unchanged.
func TestCapSessionRole(t *testing.T) {
	const p1, p2 = "0b1e5d6a-0000-4000-8000-000000000001", "0b1e5d6a-0000-4000-8000-000000000002"
	scoped := func(role string) *auth.Claims {
		return &auth.Claims{Subject: "u", LoginVia: auth.LoginViaPAT, PATProjectID: p1, PATRole: role}
	}
	cases := []struct {
		claims  *auth.Claims
		project string
		user    string
		want    string
	}{
		{scoped("viewer"), p1, "owner", "viewer"},
		{scoped("developer"), p1, "admin", "developer"},
		{scoped("admin"), p1, "developer", "developer"}, // creator demoted → token demoted
		{scoped("admin"), p1, "viewer", "viewer"},
		{scoped("admin"), p1, "", ""},                                                  // creator removed → no access
		{scoped("admin"), p2, "owner", ""},                                             // another project → no access
		{scoped("viewer"), p2, "viewer", ""},                                           // even as a member there
		{&auth.Claims{Subject: "u", LoginVia: auth.LoginViaPAT}, p2, "owner", "owner"}, // legacy token
		{&auth.Claims{Subject: "u", LoginVia: auth.LoginViaPassword}, p2, "admin", "admin"},
		{nil, p1, "admin", "admin"},
		{scoped("superuser"), p1, "owner", ""},                                        // unknown token role: no access
		{scoped("viewer"), "0B1E5D6A-0000-4000-8000-000000000001", "owner", "viewer"}, // same UUID, other spelling
	}
	for _, c := range cases {
		if got := capSessionRole(c.claims, c.project, c.user); got != c.want {
			t.Errorf("claims %+v project %s user %q: got %q, want %q", c.claims, c.project, c.user, got, c.want)
		}
	}
}
