package mailaddr

import "testing"

func TestIsBlockedSignupDomain(t *testing.T) {
	blocked := []string{
		"n16630aldiypmyl@fldubai.com",   // the reported abuse address
		"someone@fldubai.com",           // same domain, different local part
		"abuse@mail.fldubai.com",        // subdomain
		"Mixed@FLDubai.Com",             // case-insensitive
		" spaced@fldubai.com ",          // surrounding whitespace
		"trailingdot@fldubai.com.",      // trailing root dot
	}
	for _, addr := range blocked {
		if !IsBlockedSignupDomain(addr) {
			t.Errorf("IsBlockedSignupDomain(%q) = false, want true", addr)
		}
	}

	allowed := []string{
		"user@example.org",            // ordinary domain (reserved, but not blocked here)
		"user@notfldubai.com",         // substring, not a subdomain — must not match
		"user@fldubai.com.evil.com",   // blocked domain only appears mid-host
		"fldubai.com",                 // no @ at all
		"",                            // empty
	}
	for _, addr := range allowed {
		if IsBlockedSignupDomain(addr) {
			t.Errorf("IsBlockedSignupDomain(%q) = true, want false", addr)
		}
	}
}
