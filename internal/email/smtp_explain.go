package email

// Readable explanations for custom-SMTP failures. The raw errors
// ("dial tcp 1.2.3.4:587: i/o timeout", "tls: first record does not look
// like a TLS handshake") are exact but don't say what to change; the
// console shows the explanation first and the raw error after it.

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/eurobase/euroback/internal/netguard"
)

// SMTP conversation stages, in order.
const (
	StageDial      = "dial"
	StageGreeting  = "smtp client"
	StageSTARTTLS  = "starttls"
	StageAuth      = "smtp auth"
	StageMailFrom  = "MAIL FROM"
	StageRcptTo    = "RCPT TO"
	StageData      = "DATA"
	StageWriteBody = "write body"
	StageCloseData = "close DATA"
)

// SMTPError is a custom-SMTP failure and the stage it happened in.
type SMTPError struct {
	Stage      string
	Addr       string // host:port
	Encryption SenderEncryption
	Err        error
}

func (e *SMTPError) Error() string {
	if e.Stage == StageDial {
		return fmt.Sprintf("dial %s: %v", e.Addr, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Stage, e.Err)
}

func (e *SMTPError) Unwrap() error { return e.Err }

// smtpEgressBlocked: outbound SMTP from the platform is blocked by the
// hosting provider (Scaleway's default security group blocks ports 25,
// 465 and 587). Set from CUSTOM_SMTP_EGRESS_BLOCKED; while set, a
// connect timeout is explained as the block, not as a wrong host.
var smtpEgressBlocked atomic.Bool

// SetSMTPEgressBlocked records whether outbound SMTP is blocked.
func SetSMTPEgressBlocked(blocked bool) { smtpEgressBlocked.Store(blocked) }

// SMTPEgressBlocked reports whether outbound SMTP is blocked.
func SMTPEgressBlocked() bool { return smtpEgressBlocked.Load() }

// EgressBlockedNotice is shown wherever custom SMTP is configured while
// the block is in place.
const EgressBlockedNotice = "Eurobase's hosting provider currently blocks outgoing SMTP connections (ports 25, 465 and 587), so Eurobase can't reach your SMTP server yet. We're working on lifting this. Until then, disconnect custom SMTP: a verified sender is still used and its emails fail, while without one auth emails go out through Eurobase's default sender."

// DescribeSMTPError is the explanation followed by the raw error. It is
// shown to the project's developers: a DNS error names the resolver that
// answered ("lookup x on 10.0.0.10:53: …"), an internal address, so it's
// reduced to the name and the result.
func DescribeSMTPError(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		raw = fmt.Sprintf("lookup %s: %s", dnsErr.Name, dnsErr.Err)
	}
	hint := ExplainSMTPError(err)
	if hint == "" {
		return raw
	}
	return hint + " (" + raw + ")"
}

// ExplainSMTPError says, in a sentence or two, what probably went wrong
// and what to change. "" when there's nothing better than the raw error.
func ExplainSMTPError(err error) string {
	var se *SMTPError
	if !errors.As(err, &se) {
		return ""
	}
	port := ""
	if _, p, splitErr := net.SplitHostPort(se.Addr); splitErr == nil {
		port = p
	}
	msg := strings.ToLower(se.Err.Error())

	var certErr *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	certProblem := errors.As(se.Err, &certErr) || errors.As(se.Err, &hostErr) || strings.Contains(msg, "x509:")

	switch se.Stage {
	case StageDial:
		var dnsErr *net.DNSError
		var recErr tls.RecordHeaderError
		switch {
		case errors.Is(se.Err, netguard.ErrNotPublic):
			return "The host wasn't found in public DNS, or isn't a public internet address. Use the SMTP host name from your mail provider's settings."
		case errors.As(se.Err, &dnsErr):
			return "The host name wasn't found. Check its spelling — use the SMTP server name from your mail provider's settings."
		case isTimeout(se.Err):
			if SMTPEgressBlocked() {
				return fmt.Sprintf("No answer from %s. %s", se.Addr, EgressBlockedNotice)
			}
			return fmt.Sprintf("No answer from %s within the time limit. Check the host and port, and that the server accepts connections from outside your own network.", se.Addr)
		case errors.Is(se.Err, syscall.ECONNREFUSED) || strings.Contains(msg, "connection refused"):
			return fmt.Sprintf("The server refused the connection on port %s. Check the port — usually 587 with STARTTLS, or 465 with TLS.", port)
		case errors.As(se.Err, &recErr) || strings.Contains(msg, "first record does not look like a tls handshake"):
			return fmt.Sprintf("Port %s doesn't use TLS from the start. Choose STARTTLS (usually port 587), or port 465 with TLS.", port)
		case certProblem:
			return "The server's certificate isn't valid for this host name. Use the exact SMTP host name from your mail provider (not an IP address or an alias)."
		}
	case StageGreeting:
		if isTimeout(se.Err) && se.Encryption != EncryptionTLS {
			if port == "465" {
				return "Port 465 expects TLS from the start. Choose TLS as the encryption."
			}
			return "The server accepted the connection but never sent an SMTP greeting. If this port expects TLS from the start (usually 465), choose TLS."
		}
		return "The server accepted the connection but didn't respond like an SMTP server. Check the host and port."
	case StageSTARTTLS:
		if certProblem {
			return "The server's certificate isn't valid for this host name. Use the exact SMTP host name from your mail provider."
		}
		return "The server didn't accept STARTTLS on this port. Check the port, or choose TLS if the port is 465."
	case StageAuth:
		if strings.Contains(msg, "unencrypted connection") {
			return "Eurobase never sends a password over an unencrypted connection. Choose STARTTLS or TLS."
		}
		return "The server rejected the login. Check the username and password — some providers need an app-specific password for SMTP."
	case StageMailFrom:
		return "The server refused the From address. It must be an address your SMTP account may send as."
	case StageRcptTo:
		return "The server refused the recipient address. Some servers only accept recipients after login, or reject addresses they consider invalid."
	case StageData, StageWriteBody, StageCloseData:
		return "The server refused the message itself — check your provider's sending limits and content rules."
	}
	return ""
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
