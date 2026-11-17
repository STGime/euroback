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

// TestBlocklistBlocksOnlyListedDomain reproduces the exact match SignUp
// runs (SELECT ... WHERE domain = ANY(SignupDomainCandidates(email))) with
// the seeded blocklist {"fldubai.com"}, and asserts it catches that domain
// and its subdomains ONLY — every legitimate address, including close
// look-alikes, is allowed through. This is the "block one domain, never a
// real user" guarantee.
func TestBlocklistBlocksOnlyListedDomain(t *testing.T) {
	blocklist := map[string]bool{"fldubai.com": true}
	// blocked == what SignUp computes: any candidate is on the list.
	blocked := func(addr string) bool {
		for _, c := range SignupDomainCandidates(addr) {
			if blocklist[c] {
				return true
			}
		}
		return false
	}

	mustBlock := []string{
		"n16630aldiypmyl@fldubai.com", // the reported abuse address
		"anyone@fldubai.com",
		"abuse@mail.fldubai.com", // subdomain
		"x@a.b.c.fldubai.com",    // deep subdomain
		"Caps@FLDUBAI.COM",       // case
		" pad@fldubai.com ",      // whitespace
		"dot@fldubai.com.",       // trailing root dot
	}
	for _, a := range mustBlock {
		if !blocked(a) {
			t.Errorf("expected %q to be BLOCKED", a)
		}
	}

	mustAllow := []string{
		// Real providers / the maintainer's own domains.
		"user@gmail.com", "user@googlemail.com", "user@outlook.com",
		"user@proton.me", "user@yahoo.co.uk", "dev@acme.io",
		"someone@sub.gmail.com", "a@company.co.uk",
		// Look-alikes that must NOT trip the suffix/substring:
		"user@notfldubai.com",       // different registrable domain
		"user@myfldubai.com",        // prefix label, not a subdomain
		"user@fldubai.co",           // different TLD
		"user@fldubai.company.com",  // "fldubai" is just a label here
		"user@fldubai.com.evil.com", // listed domain only appears mid-host
		"user@x.fldubai.com.attacker.net",
		"fldubai.com", // no "@" — not an email
		"",
	}
	for _, a := range mustAllow {
		if blocked(a) {
			t.Errorf("expected %q to be ALLOWED (false positive — would block a legit user)", a)
		}
	}
}
