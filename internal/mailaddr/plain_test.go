package mailaddr

import "testing"

func TestIsPlain(t *testing.T) {
	ok := []string{"someone@example.org", "first.last+tag@mail.example.org", "o'neil@example.ie", "a_b-c@ex-ample.eu", "user@müller.de", "user@xn--mller-kva.de"}
	bad := []string{
		`"name"@gmail.com`, `"a b"@example.org`, `a\"b@example.org`, "a(comment)@example.org",
		"a@b@example.org", "@example.org", "someone@", "someone@localhost", "a..b@example.org",
		".a@example.org", "a.@example.org", "a b@example.org", "a@[127.0.0.1]", "a@example..org", "a\x00@example.org",
		"x@example.org.", "ｎame@gmail.com", "nаme@gmail.com", "na\u200bme@gmail.com",
	}
	for _, a := range ok {
		if !IsPlain(a) {
			t.Errorf("IsPlain(%q) = false, want true", a)
		}
	}
	for _, a := range bad {
		if IsPlain(a) {
			t.Errorf("IsPlain(%q) = true, want false", a)
		}
	}
}
