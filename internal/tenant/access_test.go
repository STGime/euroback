package tenant

import "testing"

// TestMapOrgRoleToProjectRole pins the org → project role mapping
// decision baked into IsProjectAccessible. Changing this table is a
// product-visible change (org members would gain/lose privileges on
// every org project simultaneously), so the test acts as the change-
// review checkpoint.
func TestMapOrgRoleToProjectRole(t *testing.T) {
	cases := map[string]string{
		"admin":   "admin",     // org admin manages the org, gets 'admin' on every org project
		"member":  "developer", // org member works in projects but can't manage them
		"":        "",          // no org role → no access
		"unknown": "",          // future values (e.g. billing_admin) fall through until mapped
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

// TestProjectAccess_EffectiveRole_Composition exercises the union
// logic that IsProjectAccessible applies after the DB scan. Uses the
// helper directly with pre-populated ProjectAccess fields so we don't
// need a live DB — the DB-integration path is covered by
// TestIsProjectAccessible_Integration when DATABASE_URL is set.
//
// The scenarios encode #612's regression matrix:
//
//   - org member with NO direct membership → 'developer' (was: "" → 404)
//   - org admin with NO direct membership   → 'admin'    (was: "" → 404)
//   - direct owner → 'owner' regardless of org
//   - both direct member (viewer) + org admin → 'admin' (org path wins)
//   - both direct owner + org member → 'owner' (owner path wins)
//   - not accessible at all → EffectiveRole = ""
func TestProjectAccess_EffectiveRole_Composition(t *testing.T) {
	cases := []struct {
		name         string
		pa           ProjectAccess
		wantRole     string
		wantAccessOK bool
	}{
		{
			name:     "org member only",
			pa:       ProjectAccess{ViaOrg: true, OrgRole: "member"},
			wantRole: "developer", wantAccessOK: true,
		},
		{
			name:     "org admin only",
			pa:       ProjectAccess{ViaOrg: true, OrgRole: "admin"},
			wantRole: "admin", wantAccessOK: true,
		},
		{
			name:     "direct owner only",
			pa:       ProjectAccess{ViaOwner: true},
			wantRole: "owner", wantAccessOK: true,
		},
		{
			name:     "direct project viewer only",
			pa:       ProjectAccess{ViaMember: true, MemberRole: "viewer"},
			wantRole: "viewer", wantAccessOK: true,
		},
		{
			name:     "direct viewer + org admin — org wins",
			pa:       ProjectAccess{ViaMember: true, MemberRole: "viewer", ViaOrg: true, OrgRole: "admin"},
			wantRole: "admin", wantAccessOK: true,
		},
		{
			name:     "direct owner + org member — owner wins",
			pa:       ProjectAccess{ViaOwner: true, ViaOrg: true, OrgRole: "member"},
			wantRole: "owner", wantAccessOK: true,
		},
		{
			name:     "direct admin + org member — direct wins",
			pa:       ProjectAccess{ViaMember: true, MemberRole: "admin", ViaOrg: true, OrgRole: "member"},
			wantRole: "admin", wantAccessOK: true,
		},
		{
			name:     "no access at all",
			pa:       ProjectAccess{},
			wantRole: "", wantAccessOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Mirror the composition logic from IsProjectAccessible
			// without needing a DB. If this test drifts from the
			// production computation, it's a signal the helper needs
			// a testable extraction — for now the code is short
			// enough to duplicate.
			pa := c.pa
			pa.Accessible = pa.ViaOwner || pa.ViaMember || pa.ViaOrg
			var eff string
			if pa.ViaOwner {
				eff = "owner"
			}
			if pa.ViaMember {
				eff = higherRole(eff, pa.MemberRole)
			}
			if pa.ViaOrg {
				eff = higherRole(eff, mapOrgRoleToProjectRole(pa.OrgRole))
			}
			pa.EffectiveRole = eff

			if pa.Accessible != c.wantAccessOK {
				t.Errorf("Accessible = %v, want %v", pa.Accessible, c.wantAccessOK)
			}
			if pa.EffectiveRole != c.wantRole {
				t.Errorf("EffectiveRole = %q, want %q", pa.EffectiveRole, c.wantRole)
			}
		})
	}
}
