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
