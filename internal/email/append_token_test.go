package email

import "testing"

// #258: the email link is built by appending `?token=X` (or `&token=X`
// if the tenant's redirect URL already has a query string). Broken
// here → half the tenant URLs will get a malformed link that either
// clobbers their query params or double-encodes.

func TestAppendTokenQuery(t *testing.T) {
	cases := []struct {
		redirect string
		token    string
		want     string
	}{
		// Simple URL — first query param.
		{"https://app.example.com/verify", "abc123", "https://app.example.com/verify?token=abc123"},
		// Trailing slash.
		{"https://app.example.com/verify/", "abc123", "https://app.example.com/verify/?token=abc123"},
		// URL already has a query string — must use & not ?.
		{"https://app.example.com/callback?flow=verify", "abc123", "https://app.example.com/callback?flow=verify&token=abc123"},
		// URL with multiple existing query params.
		{"https://app.example.com/x?a=1&b=2", "abc123", "https://app.example.com/x?a=1&b=2&token=abc123"},
		// URL with just `?` and no key=value (edge case; still uses &).
		{"https://app.example.com/x?", "abc123", "https://app.example.com/x?&token=abc123"},
	}
	for _, c := range cases {
		got := appendTokenQuery(c.redirect, c.token)
		if got != c.want {
			t.Errorf("appendTokenQuery(%q, %q) = %q, want %q", c.redirect, c.token, got, c.want)
		}
	}
}
