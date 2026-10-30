// Package mailaddr holds email-address checks shared by packages that
// can't import internal/email (it imports auth).
package mailaddr

import "strings"

// IsReserved reports whether addr uses a domain reserved for
// documentation and testing, which can never receive mail: the reserved
// TLDs .test, .invalid, .example and .localhost (RFC 2606 / RFC 6761) and
// the second-level example.com / example.net / example.org (RFC 2606),
// including their subdomains. Sending to them only produces bounces,
// which count against the sending domain's reputation.
func IsReserved(addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return false
	}
	domain := strings.TrimSuffix(addr[at+1:], ".")
	if domain == "localhost" {
		return true
	}
	for _, tld := range []string{".test", ".invalid", ".example", ".localhost"} {
		if strings.HasSuffix(domain, tld) {
			return true
		}
	}
	for _, d := range []string{"example.com", "example.net", "example.org"} {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}
