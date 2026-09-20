package tenant

import "testing"

// TestMapOrgRoleToProjectRole pins the org → project role mapping
// decision baked into IsProjectAccessible. Changing this table is a
// product-visible change (org members would gain/lose privileges on
// every org project simultaneously), so the test acts as the change-
// review checkpoint. See the doc comment on mapOrgRoleToProjectRole
// for the rationale behind the conservative viewer default.
func TestMapOrgRoleToProjectRole(t *testing.T) {
	cases := map[string]string{
		"admin":   "admin",  // org admin manages the org, gets 'admin' on every org project
		"member":  "viewer", // org member: read-only by default; per-project promote via chapter 19
		"":        "",       // no org role → no access
		"unknown": "",       // future values (e.g. billing_admin) fall through until mapped
	}
	for orgRole, want := range cases {
		got := mapOrgRoleToProjectRole(orgRole)
		if got != want {
			t.Errorf("mapOrgRoleToProjectRole(%q) = %q, want %q", orgRole, got, want)
		}
	}
}

// TestHigherRole covers the roleLevel-based comparison used to fold
// the direct-membership role, owner role, and org-derived role into
// a single effective role. Includes empty-string handling (unranked,
// loses to any real role).
func TestHigherRole(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"owner", "admin", "owner"},
		{"admin", "owner", "owner"},
		{"admin", "developer", "admin"},
		{"developer", "admin", "admin"},
		{"developer", "viewer", "developer"},
		{"viewer", "viewer", "viewer"},
		{"", "developer", "developer"}, // empty ranks below any real role
		{"admin", "", "admin"},
		{"", "", ""},
	}
	for _, c := range cases {
		got := higherRole(c.a, c.b)
		if got != c.want {
			t.Errorf("higherRole(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// TestComposeEffectiveRole exercises the union logic that
// IsProjectAccessible applies after the DB scan by calling the
// extracted composeEffectiveRole helper directly — no DB required,
// and (crucially) no re-implementation of the logic in the test
// body. A bug in composeEffectiveRole now fails the test.
//
// Scenarios encode #612's regression matrix + the round-1 review
// mapping change (org member → viewer, not developer):
//
//   - org member with NO direct membership → 'viewer' (was: "" → 404 before #612)
//   - org admin  with NO direct membership → 'admin'  (was: "" → 404 before #612)
//   - direct owner → 'owner' regardless of org
//   - both direct member (viewer) + org admin → 'admin' (org path wins)
//   - both direct owner + org member → 'owner' (owner path wins)
//   - both direct developer + org member (viewer) → 'developer' (direct wins)
//   - not accessible at all → EffectiveRole = ""
func TestComposeEffectiveRole(t *testing.T) {
	cases := []struct {
		name     string
		pa       ProjectAccess
		wantRole string
	}{
		{
			name:     "org member only",
			pa:       ProjectAccess{ViaOrg: true, OrgRole: "member"},
			wantRole: "viewer",
		},
		{
			name:     "org admin only",
			pa:       ProjectAccess{ViaOrg: true, OrgRole: "admin"},
			wantRole: "admin",
		},
		{
			name:     "direct owner only",
			pa:       ProjectAccess{ViaOwner: true},
			wantRole: "owner",
		},
		{
			name:     "direct project viewer only",
			pa:       ProjectAccess{ViaMember: true, MemberRole: "viewer"},
			wantRole: "viewer",
		},
		{
			name:     "direct viewer + org admin — org wins",
			pa:       ProjectAccess{ViaMember: true, MemberRole: "viewer", ViaOrg: true, OrgRole: "admin"},
			wantRole: "admin",
		},
		{
			name:     "direct owner + org member — owner wins",
			pa:       ProjectAccess{ViaOwner: true, ViaOrg: true, OrgRole: "member"},
			wantRole: "owner",
		},
		{
			name:     "direct developer + org member — direct wins",
			pa:       ProjectAccess{ViaMember: true, MemberRole: "developer", ViaOrg: true, OrgRole: "member"},
			wantRole: "developer",
		},
		{
			name:     "no access at all",
			pa:       ProjectAccess{},
			wantRole: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := composeEffectiveRole(&c.pa)
			if got != c.wantRole {
				t.Errorf("composeEffectiveRole = %q, want %q", got, c.wantRole)
			}
		})
	}

	// Nil safety — the helper should return "" not panic. Callers
	// guard against nil upstream, but a defensive fold is cheap.
	if got := composeEffectiveRole(nil); got != "" {
		t.Errorf("composeEffectiveRole(nil) = %q, want empty", got)
	}
}
