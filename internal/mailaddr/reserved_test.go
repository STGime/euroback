package mailaddr

import "testing"

func TestIsReserved(t *testing.T) {
	for addr, want := range map[string]bool{
		"user@example.com":          true,
		"User@EXAMPLE.org":          true,
		"a@mail.example.net":        true,
		"a@example.com.":            true,
		"probe@x.invalid":           true,
		"probe@app.test":            true,
		"probe@foo.example":         true,
		"probe@localhost":           true,
		"probe@app.localhost":       true,
		"daan@thekitehub.com":       false,
		"user@notexample.com":       false,
		"user@example.co":           false,
		"user@examples.com":         false,
		"user@testing.io":           false,
		"user@catchmail.io":         false,
		"not-an-address":            false,
		"user@sub.notexample.org":   false,
		"user@example.com.evil.org": false,
	} {
		if got := IsReserved(addr); got != want {
			t.Errorf("IsReserved(%q) = %v, want %v", addr, got, want)
		}
	}
}
