package mailaddr

import (
	"reflect"
	"testing"
)

func TestSignupDomainCandidates(t *testing.T) {
	cases := []struct {
		addr string
		want []string
	}{
		{"n16630aldiypmyl@fldubai.com", []string{"fldubai.com", "com"}},
		{"Mixed@FLDubai.Com", []string{"fldubai.com", "com"}},
		{" spaced@fldubai.com ", []string{"fldubai.com", "com"}},
		{"trailingdot@fldubai.com.", []string{"fldubai.com", "com"}},
		{"abuse@x.y.fldubai.com", []string{"x.y.fldubai.com", "y.fldubai.com", "fldubai.com", "com"}},
		{"user@notfldubai.com", []string{"notfldubai.com", "com"}},
		{"noatsign", nil},
		{"trailing@", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := SignupDomainCandidates(c.addr)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SignupDomainCandidates(%q) = %v, want %v", c.addr, got, c.want)
		}
	}

	// The matching contract: an address is blocked iff one of its
	// candidates equals a blocklisted registrable domain. Spot-check that
	// "fldubai.com" is a candidate for the domain and its subdomains, but
	// not for a look-alike or a domain that merely contains it mid-host.
	has := func(addr, domain string) bool {
		for _, c := range SignupDomainCandidates(addr) {
			if c == domain {
				return true
			}
		}
		return false
	}
	for _, addr := range []string{"a@fldubai.com", "a@mail.fldubai.com"} {
		if !has(addr, "fldubai.com") {
			t.Errorf("%q should match blocklist domain fldubai.com", addr)
		}
	}
	for _, addr := range []string{"a@notfldubai.com", "a@fldubai.com.evil.com"} {
		if has(addr, "fldubai.com") {
			t.Errorf("%q must NOT match blocklist domain fldubai.com", addr)
		}
	}
}
