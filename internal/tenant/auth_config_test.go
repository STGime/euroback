package tenant

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

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

func TestAppNameValidation(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"empty", "", "", true},
		{"trimmed", "  Stupid Correlations  ", "Stupid Correlations", true},
		{"unicode", "Café Zürich ☕", "Café Zürich ☕", true},
		{"max length", strings.Repeat("é", MaxAppNameLength), strings.Repeat("é", MaxAppNameLength), true},
		{"too long", strings.Repeat("a", MaxAppNameLength+1), "", false},
		{"newline", "Acme\nBcc: x@example.com", "", false},
		{"tab", "Acme\tApp", "", false},
	}
	for _, tc := range cases {
		c := DefaultAuthConfig()
		c.AppName = tc.in
		err := c.Validate()
		if tc.ok != (err == nil) {
			t.Errorf("%s: Validate() err = %v, want ok=%v", tc.name, err, tc.ok)
			continue
		}
		if tc.ok && c.AppName != tc.want {
			t.Errorf("%s: AppName = %q, want %q", tc.name, c.AppName, tc.want)
		}
	}
}

// Migration 000162 sets email_locale on every existing project, including
// ones that never saved auth settings ({}). Those must keep the defaults.
func TestParseAuthConfig_OnlyEmailLocaleKeepsDefaults(t *testing.T) {
	def := DefaultAuthConfig()
	got := ParseAuthConfig([]byte(`{"email_locale":"en"}`))
	if got.EmailLocale != "en" {
		t.Errorf("email_locale = %q", got.EmailLocale)
	}
	if strings.Join(got.RedirectURLs, ",") != strings.Join(def.RedirectURLs, ",") || strings.Join(got.CORSOrigins, ",") != strings.Join(def.CORSOrigins, ",") {
		t.Errorf("defaults lost: redirects %v cors %v, want %v / %v", got.RedirectURLs, got.CORSOrigins, def.RedirectURLs, def.CORSOrigins)
	}
	if !got.IsCORSOriginAllowed("http://localhost:3000") || got.PasswordMinLength != def.PasswordMinLength || got.SessionDuration != def.SessionDuration || len(got.Providers) != len(def.Providers) {
		t.Errorf("defaults lost: %+v", got)
	}
	// A saved config with other keys is parsed as before.
	saved := ParseAuthConfig([]byte(`{"email_locale":"de","redirect_urls":["https://app.example.com"],"cors_origins":[]}`))
	if saved.EmailLocale != "de" || len(saved.RedirectURLs) != 1 || saved.IsCORSOriginAllowed("http://localhost:3000") {
		t.Errorf("saved config: %+v", saved)
	}
}

// Migration 000162 writes the default config out in full (plus
// email_locale) for projects that never saved auth settings, so gateways
// without the email_locale special case read exactly today's defaults.
// The literal in the migration must stay equal to DefaultAuthConfig().
func TestMigration162DefaultConfig(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000162_user_email_locale.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	def, _ := json.Marshal(DefaultAuthConfig())
	if !strings.Contains(string(raw), "'"+string(def)+"'::jsonb") {
		t.Fatalf("000162 must contain the default config %s", def)
	}
	// What the migration stores, read the way a gateway without the
	// special case reads it: a plain unmarshal (then the zero-value fills).
	var merged map[string]any
	_ = json.Unmarshal(def, &merged)
	merged["email_locale"] = "en"
	stored, _ := json.Marshal(merged)
	var plain AuthConfig
	if err := json.Unmarshal(stored, &plain); err != nil {
		t.Fatal(err)
	}
	want := DefaultAuthConfig()
	want.EmailLocale = "en"
	if !reflect.DeepEqual(plain, want) {
		t.Errorf("plain read of the stored config = %+v, want the defaults %+v", plain, want)
	}
	if got := ParseAuthConfig(stored); !reflect.DeepEqual(got, want) {
		t.Errorf("ParseAuthConfig = %+v, want %+v", got, want)
	}
}
