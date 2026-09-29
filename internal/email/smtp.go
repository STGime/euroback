package email

// Custom-SMTP send path (#235 Part 1). Used when a project has a
// configured + verified ProjectSender — `EmailService.send` routes here
// instead of the shared Scaleway TEM client.
//
// Why net/smtp and not a richer library
// =====================================
// net/smtp is stdlib, has been stable since Go 1.0, and supports both
// STARTTLS upgrade (RFC 3207) and direct TLS (SMTPS, port 465). Both
// modes cover what every common SMTP provider exposes. The only thing
// it doesn't do well is the very long-deprecated CRAM-MD5 path, which
// no modern provider expects. Avoiding a third-party SMTP library
// keeps the supply chain narrow.
//
// MIME shape
// ==========
// We emit a single-part `text/html; charset=UTF-8` message with the
// minimum headers a receiving MTA needs. No multipart/alternative
// (text-plain fallback) yet — the templates package ships HTML only.
// If a customer pipes a plain-text-only template through here it will
// still arrive as text/html with no <html> wrapper; most clients
// render that as plain text. Adding multipart is straightforward when
// a customer asks; not worth the boilerplate today.
//
// Timeout
// =======
// Two layers: the dialer's own connect timeout (10s) plus a
// post-dial deadline of 15s applied to the connection for the rest
// of the conversation (greeting + STARTTLS + auth + DATA + quit).
// Total worst-case ~25s, but the post-dial budget starts fresh after
// the connect — so a slow handshake from a healthy provider doesn't
// blow up because the dial ate most of the budget (review #15).

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/netguard"
)

// smtpDialer connects to custom SMTP servers — public addresses only;
// tests replace its Lookup / Dial.
var smtpDialer = &netguard.Dialer{Timeout: customSMTPDialTimeout}

// allowedSMTPPorts are the mail submission ports: 587 (STARTTLS), 465
// (TLS) and 2525 (the alternative some providers offer). Port 25 is for
// server-to-server delivery, not for sending as an account.
var allowedSMTPPorts = map[int]bool{465: true, 587: true, 2525: true}

// checkSenderRules: public host, a submission port, encryption.
func checkSenderRules(host string, port int, enc SenderEncryption) error {
	if err := netguard.CheckHostName(host); err != nil {
		return errors.New("SMTP host must be a public internet address (a host name like smtp.example.com)")
	}
	if !allowedSMTPPorts[port] {
		return fmt.Errorf("SMTP port must be 587 (STARTTLS), 465 (TLS) or 2525, got %d", port)
	}
	if enc != EncryptionSTARTTLS && enc != EncryptionTLS {
		return errors.New("SMTP encryption must be STARTTLS or TLS")
	}
	return nil
}

// senderUsable: the sender meets the rules and has a login.
func senderUsable(sender *ProjectSender) error {
	if err := checkSenderRules(sender.Host, sender.Port, sender.Encryption); err != nil {
		return err
	}
	if sender.Username == "" || sender.Password == "" {
		return errors.New("SMTP login required: set the username and password of your SMTP account")
	}
	return nil
}

// customSMTPDialTimeout caps the TCP dial. Separate from the
// post-dial budget so a slow handshake doesn't get charged the dial
// time (review #15).
const customSMTPDialTimeout = 10 * time.Second

// customSMTPPostDialBudget is the deadline applied to the conn after
// it's open, covering greeting + STARTTLS + auth + DATA + quit.
const customSMTPPostDialBudget = 15 * time.Second

// sendViaCustomSMTP dials the configured provider, authenticates, and
// sends a single HTML message. The sender's plaintext password must
// already be populated (LoadForSend does the decrypt).
//
// Errors are wrapped with the high-level stage that failed (dial,
// starttls, auth, send) so the console can show "auth failed" vs
// "dial failed" without digging through the underlying net.OpError.
func sendViaCustomSMTP(ctx context.Context, sender *ProjectSender, to, subject, htmlBody string) error {
	if sender == nil {
		return fmt.Errorf("sendViaCustomSMTP: nil sender")
	}
	// Checked on every send, not only on save: rows saved before these
	// rules existed must not be used either.
	if err := senderUsable(sender); err != nil {
		return err
	}

	host := netguard.Normalize(sender.Host)
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", sender.Port))

	// Only ever to a public address: resolved and checked here, and the
	// connection goes to the checked address (see internal/netguard).
	conn, err := smtpDialer.DialContext(ctx, host, fmt.Sprintf("%d", sender.Port))
	if err != nil {
		return &SMTPError{Stage: StageDial, Addr: addr, Encryption: sender.Encryption, Err: err}
	}
	if sender.Encryption == EncryptionTLS {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		})
		hsCtx, cancelHS := context.WithTimeout(ctx, customSMTPDialTimeout)
		err := tlsConn.HandshakeContext(hsCtx)
		cancelHS()
		if err != nil {
			conn.Close()
			return &SMTPError{Stage: StageDial, Addr: addr, Encryption: sender.Encryption, Err: err}
		}
		conn = tlsConn
	}
	defer conn.Close()

	// Post-dial deadline covers greeting + STARTTLS + auth + DATA +
	// quit. Computed AFTER the dial completes so a slow handshake
	// from a healthy provider doesn't get clipped because the dial
	// ate most of the budget.
	_ = conn.SetDeadline(time.Now().Add(customSMTPPostDialBudget))

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return &SMTPError{Stage: StageGreeting, Addr: addr, Encryption: sender.Encryption, Err: err}
	}
	defer client.Quit() //nolint:errcheck — best-effort cleanup

	if sender.Encryption == EncryptionSTARTTLS {
		if err := client.StartTLS(&tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		}); err != nil {
			return &SMTPError{Stage: StageSTARTTLS, Addr: addr, Encryption: sender.Encryption, Err: err}
		}
	}

	// senderUsable guarantees a login.
	if sender.Username != "" || sender.Password != "" {
		auth := smtp.PlainAuth("", sender.Username, sender.Password, host)
		if err := client.Auth(auth); err != nil {
			return &SMTPError{Stage: StageAuth, Addr: addr, Encryption: sender.Encryption, Err: err}
		}
	}

	if err := client.Mail(sender.FromEmail); err != nil {
		return &SMTPError{Stage: StageMailFrom, Addr: addr, Encryption: sender.Encryption, Err: err}
	}
	if err := client.Rcpt(to); err != nil {
		return &SMTPError{Stage: StageRcptTo, Addr: addr, Encryption: sender.Encryption, Err: err}
	}

	wc, err := client.Data()
	if err != nil {
		return &SMTPError{Stage: StageData, Addr: addr, Encryption: sender.Encryption, Err: err}
	}
	if _, err := wc.Write(buildMIMEMessage(sender, to, subject, htmlBody)); err != nil {
		wc.Close()
		return &SMTPError{Stage: StageWriteBody, Addr: addr, Encryption: sender.Encryption, Err: err}
	}
	if err := wc.Close(); err != nil {
		return &SMTPError{Stage: StageCloseData, Addr: addr, Encryption: sender.Encryption, Err: err}
	}
	return nil
}

// buildMIMEMessage assembles the RFC 5322 headers + HTML body. Kept
// separate from the send so a future text-plain fallback or attachment
// support has a focused place to live.
func buildMIMEMessage(sender *ProjectSender, to, subject, htmlBody string) []byte {
	var fromHeader string
	if sender.FromName != "" {
		fromHeader = fmt.Sprintf(`"%s" <%s>`, escapeQuotes(sender.FromName), sender.FromEmail)
	} else {
		fromHeader = sender.FromEmail
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/html; charset=UTF-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: 8bit\r\n")
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}

// escapeQuotes lets us safely interpolate a display-name into a
// double-quoted From header. SMTP RFC 5322 lets us put practically
// anything inside the quotes except backslash and bare double-quote;
// we escape both. Without this, a From Name like
// `"Foo "Bar" Baz"` would corrupt the header.
func escapeQuotes(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(s)
}
