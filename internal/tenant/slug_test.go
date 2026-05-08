package tenant

import "testing"

// Closes #49: tenant slugs collide with platform-reserved subdomains.

func TestSlugIsReserved(t *testing.T) {
	for _, s := range []string{"www", "admin", "api", "app", "auth", "console", "superadmin", "eurobase", "mail", "static"} {
		if !slugIsReserved(s) {
			t.Errorf("slugIsReserved(%q) = false, want true", s)
		}
	}
	// Mixed case still flagged (lowercase normalised).
	if !slugIsReserved("WWW") {
		t.Errorf("slugIsReserved should be case-insensitive on input")
	}
	for _, s := range []string{"my-app", "newtek", "forestdream", "user-data", "alpha"} {
		if slugIsReserved(s) {
			t.Errorf("slugIsReserved(%q) = true, want false", s)
		}
	}
}

func TestSlugify_AppendsSuffixWhenReserved(t *testing.T) {
	for _, name := range []string{"Admin", "WWW", "API", "Console"} {
		got := slugify(name)
		if slugIsReserved(got) {
			t.Errorf("slugify(%q) = %q, still reserved", name, got)
		}
		if got == "" {
			t.Errorf("slugify(%q) returned empty", name)
		}
	}
}

func TestSlugify_PassesNonReservedThrough(t *testing.T) {
	cases := map[string]string{
		"My App":       "my-app",
		"forest dream": "forest-dream",
		"UPPERCASE":    "uppercase",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
