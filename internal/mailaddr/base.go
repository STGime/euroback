package mailaddr

import "strings"

// Base returns the mailbox an address delivers to, for telling apart
// accounts that share one inbox: lowercased, the "+tag" removed from the
// local part (subaddressing, supported by most providers), and for Gmail
// the dots removed and googlemail.com folded into gmail.com (Gmail ignores
// dots). "A.B+x@GoogleMail.com" → "ab@gmail.com". An address without "@"
// is returned lowercased and trimmed.
//
// Used to rate-limit signups per mailbox and to refuse a signup whose
// mailbox belongs to a suspended account. It never changes the address an
// account is stored or mailed under.
func Base(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return addr
	}
	local, domain := addr[:at], strings.TrimSuffix(addr[at+1:], ".")
	if i := strings.IndexByte(local, '+'); i > 0 {
		local = local[:i]
	}
	if domain == "gmail.com" || domain == "googlemail.com" {
		domain = "gmail.com"
		local = strings.ReplaceAll(local, ".", "")
	}
	return local + "@" + domain
}
