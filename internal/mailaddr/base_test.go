package mailaddr

import "testing"

func TestBase(t *testing.T) {
	cases := map[string]string{
		"someone@example.org":          "someone@example.org",
		" Someone+news@Example.ORG ":   "someone@example.org",
		"some.one+x1@gmail.com":        "someone@gmail.com",
		"S.O.M.E.O.N.E@googlemail.com": "someone@gmail.com",
		"some.one+a+b@gmail.com":       "someone@gmail.com",
		"first.last+tag@example.org":   "first.last@example.org", // dots only matter on Gmail
		"+tag@example.org":             "+tag@example.org",       // no local part left: keep as is
		"someone@gmail.com.":           "someone@gmail.com",
		"not-an-address":               "not-an-address",
		"a@b@gmail.com":                "a@b@gmail.com",
		`"name"@gmail.com`:             "name@gmail.com",
		`"a.b+x"@googlemail.com`:       "ab@gmail.com",
		`"a\"b"@example.org`:           `a"b@example.org`, // an escaped quote is a character of its own
	}
	for in, want := range cases {
		if got := Base(in); got != want {
			t.Errorf("Base(%q) = %q, want %q", in, got, want)
		}
	}
}
