package email

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// maxProjectSenderName caps the project part of the sender name, so the
// platform's own name ("via Eurobase") always stays visible in a mail
// client's sender column.
const maxProjectSenderName = 40

// ProjectSenderName is the sender name of a project's auth email sent
// through the platform sender: "<project> via <platform>", e.g.
// "Innebandy pilot test via Eurobase". The recipient signed up to the
// project, so a sender that names it is one they recognise; the address
// stays the platform's (SPF / DKIM / DMARC aligned), and "via" keeps it
// clear who sends it. Returns "" (keep the platform's own name) when the
// project name has nothing printable left.
//
// The project part is reduced to printable characters on one line:
// it is first folded with NFKC (so fullwidth "＠" / "＜" become the
// characters they look like and are filtered below); line breaks and
// tabs become spaces, other control and format characters (incl. bidi
// overrides) are dropped, characters with a meaning in an address header
// (" < > \ , ; : @ ( ) [ ]) and "=" (no RFC 2047 encoded word "=?…?=",
// which a mail client would decode into those characters) become spaces,
// runs of spaces collapse, and it is cut at maxProjectSenderName
// characters.
func ProjectSenderName(project, platform string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFKC.String(project) {
		switch {
		case unicode.IsSpace(r), strings.ContainsRune(`"<>\,;:@()[]=`, r):
			space = true
			continue
		case r == utf8.RuneError,
			unicode.IsControl(r),
			unicode.Is(unicode.Cf, r):
			continue
		case !unicode.IsPrint(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	name := b.String()
	if utf8.RuneCountInString(name) > maxProjectSenderName {
		name = strings.TrimSpace(string([]rune(name)[:maxProjectSenderName])) + "…"
	}
	if name == "" {
		return ""
	}
	platform = strings.TrimSpace(platform)
	if platform == "" {
		platform = "Eurobase"
	}
	return name + " via " + platform
}

// rejectedSenderName reports whether a send failed because the provider
// refused the request as invalid (400 / 422) — the case a sender name it
// doesn't accept would cause. Auth, rate-limit and server errors are not
// retried.
func rejectedSenderName(err error) bool {
	var te *TEMError
	return errors.As(err, &te) && (te.Status == http.StatusBadRequest || te.Status == http.StatusUnprocessableEntity)
}
