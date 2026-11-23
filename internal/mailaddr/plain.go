package mailaddr

import "strings"

// IsPlain reports whether addr is a plain mailbox address: one "@", a
// non-empty local part without quoting, escapes, comments or spaces, no
// empty dot-separated parts, and a domain with at least one dot.
//
// RFC 5321 also allows quoted local parts ("john"@example.org), which
// most providers deliver to the same inbox as the unquoted form. Nobody
// needs them to sign up, and refusing them keeps one address = one
// spelling for the per-mailbox checks (Base).
func IsPlain(addr string) bool {
	at := strings.IndexByte(addr, '@')
	if at <= 0 || at != strings.LastIndexByte(addr, '@') {
		return false
	}
	local, domain := addr[:at], addr[at+1:]
	if strings.ContainsAny(addr, "\"\\()<>,;:[] \t") {
		return false
	}
	for _, r := range addr {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	// Local part: ASCII only (no lookalike, zero-width or format
	// characters). Internationalised domains stay allowed.
	for _, r := range local {
		if r > 0x7e {
			return false
		}
	}
	for _, part := range strings.Split(local, ".") {
		if part == "" {
			return false
		}
	}
	if strings.HasSuffix(domain, ".") || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" {
			return false
		}
	}
	return true
}
