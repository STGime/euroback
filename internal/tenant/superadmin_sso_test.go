package tenant

import (
	"context"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A superadmin passes an org's sso_required only with a passkey session
// AND the account check (still superadmin, passkey older than a day);
// everyone else only with an SSO session for that org.
func TestClaimsSatisfySSOFor(t *testing.T) {
	const org, other = "org-a", "org-b"
	verified := true
	orig := superadminBypassVerifier
	superadminBypassVerifier = func(context.Context, *pgxpool.Pool, string) bool { return verified }
	defer func() { superadminBypassVerifier = orig }()
	pool := &pgxpool.Pool{} // never used: the verifier is replaced
	ctx := context.Background()

	cases := []struct {
		name     string
		claims   *auth.Claims
		required bool
		verified bool
		pool     *pgxpool.Pool
		want     bool
	}{
		{"not required", &auth.Claims{LoginVia: auth.LoginViaPassword}, false, true, pool, true},
		{"sso for this org", &auth.Claims{LoginVia: auth.LoginViaSSO, SsoOrgID: org}, true, false, pool, true},
		{"sso for another org", &auth.Claims{LoginVia: auth.LoginViaSSO, SsoOrgID: other}, true, true, pool, false},
		{"superadmin, sso for another org", &auth.Claims{LoginVia: auth.LoginViaSSO, SsoOrgID: other, IsSuperadmin: true}, true, true, pool, false},
		{"password", &auth.Claims{LoginVia: auth.LoginViaPassword}, true, true, pool, false},
		{"passkey, not superadmin", &auth.Claims{LoginVia: auth.LoginViaPasskey}, true, true, pool, false},
		{"superadmin, password", &auth.Claims{LoginVia: auth.LoginViaPassword, IsSuperadmin: true}, true, true, pool, false},
		{"superadmin, empty login_via", &auth.Claims{IsSuperadmin: true}, true, true, pool, false},
		{"superadmin, token", &auth.Claims{LoginVia: auth.LoginViaPAT, IsSuperadmin: true}, true, true, pool, false},
		{"superadmin, passkey, account checks out", &auth.Claims{LoginVia: auth.LoginViaPasskey, IsSuperadmin: true}, true, true, pool, true},
		{"superadmin, passkey, revoked or passkey too new", &auth.Claims{LoginVia: auth.LoginViaPasskey, IsSuperadmin: true}, true, false, pool, false},
		{"superadmin, passkey, no developer pool", &auth.Claims{LoginVia: auth.LoginViaPasskey, IsSuperadmin: true}, true, true, nil, false},
		{"no claims", nil, true, true, pool, false},
	}
	for _, c := range cases {
		verified = c.verified
		if got := ClaimsSatisfySSOFor(ctx, c.pool, c.claims, org, c.required); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Reads are noted once per (user, org, project) per hour; changes every
// time; real SSO sessions never.
func TestNoteSuperadminSSOBypass_Dedupe(t *testing.T) {
	superadminSSONotedMu.Lock()
	superadminSSONoted = map[string]time.Time{}
	superadminSSONotedMu.Unlock()
	ctx := context.Background()
	sa := &auth.Claims{Subject: "u1", LoginVia: auth.LoginViaPasskey, IsSuperadmin: true}

	noteSuperadminSSOBypass(ctx, sa, "org", "p1", false, nil)
	noteSuperadminSSOBypass(ctx, sa, "org", "p1", false, nil)
	noteSuperadminSSOBypass(ctx, sa, "org", "p2", false, nil)
	noteSuperadminSSOBypass(ctx, sa, "org", "", true, nil) // change: not keyed
	noteSuperadminSSOBypass(ctx, &auth.Claims{Subject: "u2", LoginVia: auth.LoginViaSSO, SsoOrgID: "org"}, "org", "p1", false, nil)

	superadminSSONotedMu.Lock()
	defer superadminSSONotedMu.Unlock()
	if len(superadminSSONoted) != 2 {
		t.Fatalf("want 2 deduped read keys (p1, p2), got %v", superadminSSONoted)
	}
}
