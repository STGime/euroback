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
//
// A name that looks like Eurobase itself or a well-known brand (payment,
// mail, big tech, banks, parcel services — impersonatesBrand) also gets
// "" so the email goes out under the platform's own name: a project
// can't send "PayPal Security via Eurobase". Only the sender name; the
// name in the email text is unchanged.
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
	if name == "" || impersonatesBrand(name) {
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

// brandWords are matched as whole words of a name; brandSquashed also
// inside the name with spaces, punctuation and digit look-alikes removed
// ("Pay Pal", "g00gle"), so only names long and specific enough not to
// hit ordinary words.
var (
	brandWords = []string{
		"eurobase", "scaleway",
		"paypal", "stripe", "mollie", "klarna", "revolut", "n26", "visa", "mastercard", "amex",
		"swish", "vipps", "mobilepay", "bankid", "itsme", "ideal", "twint",
		"apple", "icloud", "google", "gmail", "microsoft", "outlook", "hotmail", "office365", "live",
		"amazon", "aws", "facebook", "instagram", "whatsapp", "netflix", "linkedin", "spotify",
		"dropbox", "docusign", "adobe", "ebay", "steam", "binance", "coinbase",
		"dhl", "fedex", "postnord", "gls", "dpd",
	}
	brandSquashed = []string{
		"eurobase", "scaleway", "paypal", "mollie", "klarna", "revolut", "mastercard", "bankid",
		"icloud", "google", "gmail", "microsoft", "outlook", "hotmail", "office365",
		"amazon", "facebook", "instagram", "whatsapp", "netflix", "linkedin", "spotify",
		"dropbox", "docusign", "binance", "coinbase", "postnord",
	}
	leet = strings.NewReplacer("0", "o", "1", "l", "3", "e", "4", "a", "5", "s", "7", "t", "$", "s", "|", "l", "@", "a")
)

// impersonatesBrand reports whether name (already NFKC-folded) names
// Eurobase or a well-known brand. Case and accents are ignored.
func impersonatesBrand(name string) bool {
	var folded strings.Builder
	for _, r := range norm.NFKD.String(strings.ToLower(name)) {
		if !unicode.Is(unicode.Mn, r) {
			folded.WriteRune(r)
		}
	}
	f := folded.String()
	words := strings.FieldsFunc(f, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, w := range words {
		for _, b := range brandWords {
			if w == b {
				return true
			}
		}
	}
	squashed := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		if r < 128 && strings.ContainsRune("013457$|@", r) {
			return r
		}
		return -1
	}, f)
	squashed = leet.Replace(squashed)
	for _, b := range brandSquashed {
		if strings.Contains(squashed, b) {
			return true
		}
	}
	return false
}
