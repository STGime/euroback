package auth

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

// ErrNoMailDomain refuses a platform signup whose email domain can't
// receive mail: it doesn't exist, has neither MX nor A/AAAA records, or
// publishes a null MX (RFC 7505). The verification email could never
// arrive, and sending it would only bounce.
var ErrNoMailDomain = errors.New("this address's domain can't receive email — check the address")

// mailDomainResolver is the part of net.Resolver the check uses.
type mailDomainResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// mailDomainCheckTimeout bounds both lookups together.
const mailDomainCheckTimeout = 3 * time.Second

// EnableMailDomainCheck turns on the signup check that the email domain can
// receive mail (SIGNUP_MX_CHECK). Off by default, so tests and dev don't
// depend on DNS.
func (s *PlatformAuthService) EnableMailDomainCheck(r mailDomainResolver) {
	s.mailDomains = r
}

// mailDomainVerdict is the outcome of checkMailDomain.
type mailDomainVerdict int

const (
	mailDomainOK      mailDomainVerdict = iota // has MX, or A/AAAA
	mailDomainNone                             // can't receive mail
	mailDomainUnknown                          // lookup failed (timeout, server error)
)

// checkMailDomain decides whether domain can receive mail. Only a definite
// DNS answer refuses: NXDOMAIN or no records → mailDomainNone; a lookup
// error (timeout, SERVFAIL, our resolver down) → mailDomainUnknown, which
// the caller lets through (abuse mitigation must not block real signups
// during a DNS problem on our side).
func checkMailDomain(ctx context.Context, r mailDomainResolver, domain string) (mailDomainVerdict, error) {
	// Go's resolver doesn't convert internationalised names: it reports a
	// non-ASCII name as "not found" without asking. Look up the punycode
	// form; a name that can't be converted is "unknown" (allowed).
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil || ascii == "" {
		return mailDomainUnknown, err
	}
	// An absolute name, so the pod's DNS search list (cluster-internal
	// suffixes) is never tried: fewer queries, and internal names can't
	// pass as mail domains.
	name := strings.TrimSuffix(ascii, ".") + "."

	ctx, cancel := context.WithTimeout(ctx, mailDomainCheckTimeout)
	defer cancel()

	mxs, err := r.LookupMX(ctx, name)
	switch {
	case err == nil:
		nullMX := false
		for _, mx := range mxs {
			if strings.TrimSuffix(mx.Host, ".") != "" {
				return mailDomainOK, nil
			}
			nullMX = true
		}
		if nullMX {
			return mailDomainNone, nil // RFC 7505: "this domain accepts no mail"
		}
	case isDNSNotFound(err):
		// No MX: mail falls back to the domain's address records.
	default:
		return mailDomainUnknown, err
	}

	hosts, err := r.LookupHost(ctx, name)
	switch {
	case err == nil && len(hosts) > 0:
		return mailDomainOK, nil
	case err == nil || isDNSNotFound(err):
		return mailDomainNone, nil
	default:
		return mailDomainUnknown, err
	}
}

func isDNSNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// refuseUndeliverableDomain runs the check for a signup address when it is
// enabled. It returns ErrNoMailDomain only on a definite answer; a failed
// lookup is logged and allowed.
func (s *PlatformAuthService) refuseUndeliverableDomain(ctx context.Context, email string) error {
	if s.mailDomains == nil {
		return nil
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return nil
	}
	domain := strings.TrimSuffix(strings.ToLower(email[at+1:]), ".")
	verdict, err := checkMailDomain(ctx, s.mailDomains, domain)
	switch verdict {
	case mailDomainNone:
		slog.Info("signup refused: email domain can't receive mail", "domain", domain)
		return ErrNoMailDomain
	case mailDomainUnknown:
		slog.Warn("signup mail domain check failed; allowing signup", "domain", domain, "error", err)
	}
	return nil
}
