package mailaddr

import "strings"

// SignupDomainCandidates returns an address's email domain together with
// each of its parent domains, lowercased, for matching against the signup
// blocklist (public.platform_blocked_signup_domains). The blocklist stores
// registrable domains, so a row for "fldubai.com" must also refuse
// "x@mail.fldubai.com": the caller queries `domain = ANY(candidates)`, and
// a subdomain's candidate list includes its parents.
//
// Example: "a@x.y.fldubai.com" -> ["x.y.fldubai.com", "y.fldubai.com",
// "fldubai.com", "com"]. Returns nil when addr has no domain part.
//
// The actual lookup lives in the auth package (PlatformAuthService.SignUp)
// because mailaddr is a leaf package that must not import a DB driver; this
// helper is the pure string half of the check.
func SignupDomainCandidates(addr string) []string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return nil
	}
	domain := strings.TrimSuffix(addr[at+1:], ".")
	if domain == "" {
		return nil
	}
	var out []string
	for {
		out = append(out, domain)
		i := strings.IndexByte(domain, '.')
		if i < 0 {
			break
		}
		domain = domain[i+1:]
	}
	return out
}
