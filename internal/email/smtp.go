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
// 15s end-to-end (dial + auth + send + quit). Longer than the TEM
// client's 10s because some upstream providers do greylist-style
// delays on first contact; we don't want a healthy provider to fail
// because we cut the connection at 10.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// customSMTPTimeout is the dial + handshake + send + quit budget. See
// the package header for why it's longer than the TEM client.
const customSMTPTimeout = 15 * time.Second

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
	addr := net.JoinHostPort(sender.Host, fmt.Sprintf("%d", sender.Port))

	dialer := &net.Dialer{Timeout: customSMTPTimeout}

	var (
		conn net.Conn
		err  error
	)
	switch sender.Encryption {
	case EncryptionTLS:
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName: sender.Host,
			MinVersion: tls.VersionTLS12,
		})
	default: // starttls and none both start in plaintext
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	// Deadline covers the whole conversation.
	deadline := time.Now().Add(customSMTPTimeout)
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, sender.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Quit() //nolint:errcheck — best-effort cleanup

	if sender.Encryption == EncryptionSTARTTLS {
		if err := client.StartTLS(&tls.Config{
			ServerName: sender.Host,
			MinVersion: tls.VersionTLS12,
		}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	// Authenticate if the provider needs it. Bare-relay (no username +
	// no password) is supported for the rare internal-relay case.
	if sender.Username != "" || sender.Password != "" {
		auth := smtp.PlainAuth("", sender.Username, sender.Password, sender.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(sender.FromEmail); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := wc.Write(buildMIMEMessage(sender, to, subject, htmlBody)); err != nil {
		wc.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close DATA: %w", err)
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
