package mailaddr

import "strings"

// blockedSignupDomains lists deliverable domains that are refused at
// platform signup because the addresses on them only ever produce abuse
// or throwaway accounts, never a genuine user. Unlike IsReserved (which
// covers undeliverable reserved/test TLDs), these are real domains we
// deliberately decline. Keep entries lowercase, registrable domains
// (no leading dot); matching also covers their subdomains. Sorted for
// readability.
var blockedSignupDomains = []string{
	"fldubai.com",
}

// IsBlockedSignupDomain reports whether addr's domain is on the signup
// blocklist — the domain itself, or any subdomain of it. The caller
// (PlatformAuthService.SignUp) refuses the signup when this is true.
func IsBlockedSignupDomain(addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return false
	}
	domain := strings.TrimSuffix(addr[at+1:], ".")
	for _, d := range blockedSignupDomains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}
