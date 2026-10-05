package tenant

import "testing"

// Closes #48: OAuth redirect-URL allowlist must require segment
// boundary, not substring HasPrefix.

func TestIsRedirectURLAllowed_PathBoundaryEnforced(t *testing.T) {
	cfg := &AuthConfig{
		RedirectURLs: []string{
			"https://app.example.com/cb",
			"http://localhost:3000",
		},
	}
	cases := []struct {
		name string
		url  string
		want bool
	}{
		// Allowed
		{"exact path match", "https://app.example.com/cb", true},
		{"deeper segment under allowed path", "https://app.example.com/cb/sub", true},
		{"deeper segment with multiple levels", "https://app.example.com/cb/auth/done", true},
		{"empty path under root-allowed origin", "http://localhost:3000", true},
		{"deeper path under root-allowed origin", "http://localhost:3000/anything/here", true},

		// Rejected — segment-boundary cases the previous HasPrefix accepted
		{"path-confusion: cb-evil", "https://app.example.com/cb-evil", false},
		{"path-confusion: cb-evil/sub", "https://app.example.com/cb-evil/sub", false},
		{"path-confusion: cb..", "https://app.example.com/cb..", false},
		{"path-confusion: cb.txt", "https://app.example.com/cb.txt", false},

		// Rejected — different host / scheme
		{"different host", "https://attacker.com/cb", false},
		{"different scheme", "http://app.example.com/cb", false},

		// Rejected — bad URLs
		{"empty", "", false},
		{"no scheme", "//app.example.com/cb", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.IsRedirectURLAllowed(tc.url)
			if got != tc.want {
				t.Errorf("IsRedirectURLAllowed(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestIsRedirectURLAllowed_RootAllowedHostAcceptsAnyPath(t *testing.T) {
	cfg := &AuthConfig{RedirectURLs: []string{"https://app.example.com"}}
	for _, p := range []string{
		"https://app.example.com",
		"https://app.example.com/",
		"https://app.example.com/anything",
		"https://app.example.com/deep/nested/path",
	} {
		if !cfg.IsRedirectURLAllowed(p) {
			t.Errorf("expected %q to be allowed under root-allowed origin", p)
		}
	}
}

// #630: passkey (WebAuthn) relying-party config validation + IsPasskeyEnabled.
func TestPasskeyConfigValidation(t *testing.T) {
	// IsPasskeyEnabled: needs enabled + rp_id + >=1 origin.
	cases := []struct {
		name string
		pk   *PasskeyConfig
		want bool
	}{
		{"nil", nil, false},
		{"disabled", &PasskeyConfig{Enabled: false, RPID: "app.example.com", Origins: []string{"https://app.example.com"}}, false},
		{"no rp_id", &PasskeyConfig{Enabled: true, Origins: []string{"https://app.example.com"}}, false},
		{"no origins", &PasskeyConfig{Enabled: true, RPID: "app.example.com"}, false},
		{"ok", &PasskeyConfig{Enabled: true, RPID: "app.example.com", Origins: []string{"https://app.example.com"}}, true},
	}
	for _, tc := range cases {
		c := &AuthConfig{Passkeys: tc.pk}
		if got := c.IsPasskeyEnabled(); got != tc.want {
			t.Errorf("%s: IsPasskeyEnabled = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Validate: origin host must be rp_id or a subdomain of it, no path.
	valCases := []struct {
		name    string
		pk      *PasskeyConfig
		wantErr bool
	}{
		{"exact host", &PasskeyConfig{Enabled: true, RPID: "example.com", Origins: []string{"https://example.com"}}, false},
		{"subdomain", &PasskeyConfig{Enabled: true, RPID: "example.com", Origins: []string{"https://app.example.com"}}, false},
		{"localhost dev", &PasskeyConfig{Enabled: true, RPID: "localhost", Origins: []string{"http://localhost:3000"}}, false},
		{"foreign host", &PasskeyConfig{Enabled: true, RPID: "example.com", Origins: []string{"https://attacker.com"}}, true},
		{"suffix-not-subdomain", &PasskeyConfig{Enabled: true, RPID: "example.com", Origins: []string{"https://notexample.com"}}, true},
		{"origin with path", &PasskeyConfig{Enabled: true, RPID: "example.com", Origins: []string{"https://example.com/cb"}}, true},
		{"enabled no rp_id", &PasskeyConfig{Enabled: true, Origins: []string{"https://example.com"}}, true},
	}
	for _, tc := range valCases {
		c := DefaultAuthConfig() // email_password enabled → passes the rest
		c.Passkeys = tc.pk
		err := c.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Validate err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}
