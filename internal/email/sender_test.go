package email

import (
	"strings"
	"testing"
)

// TestSovereigntyWarningFor pins the US-provider classifier so that
// adding/removing entries doesn't silently change the warning shape.
// The list intentionally only catches the obvious cases — a long
// allow/deny list of every email provider on earth is a losing game.
func TestSovereigntyWarningFor(t *testing.T) {
	cases := []struct {
		host     string
		wantUS   bool
		mustHave string
	}{
		// US providers — must warn.
		{"smtp.sendgrid.net", true, "SendGrid"},
		{"smtp.SENDGRID.net", true, "SendGrid"}, // case-insensitive
		{"smtp.mailgun.org", true, "Mailgun"},
		{"smtp.mailgun.net", true, "Mailgun"},
		{"smtp.postmarkapp.com", true, "Postmark"},
		{"email-smtp.us-west-2.amazonaws.com", true, "Amazon SES"},
		{"smtp.sparkpostmail.com", true, "SparkPost"},

		// EU / unknown providers — no warning.
		{"smtp.scaleway.com", false, ""},
		{"smtp.brevo.com", false, ""},
		{"in.mailjet.com", false, ""},
		{"smtp.eu.example.com", false, ""},
		{"", false, ""},
		{"   ", false, ""},
	}
	for _, c := range cases {
		got := sovereigntyWarningFor(c.host)
		if c.wantUS {
			if got == "" {
				t.Errorf("sovereigntyWarningFor(%q) = empty, want US warning", c.host)
				continue
			}
			if !strings.Contains(got, c.mustHave) {
				t.Errorf("sovereigntyWarningFor(%q) = %q, want substring %q", c.host, got, c.mustHave)
			}
		} else if got != "" {
			t.Errorf("sovereigntyWarningFor(%q) = %q, want empty", c.host, got)
		}
	}
}

// TestValidateUpsert pins the shape-check so an operator typo surfaces
// before the seal/DB write rather than as a confusing constraint-violation
// from Postgres.
func TestValidateUpsert(t *testing.T) {
	good := UpsertRequest{
		Host:       "smtp.example.com",
		Port:       587,
		Username:   "user",
		FromEmail:  "noreply@example.com",
		Encryption: EncryptionSTARTTLS,
	}
	if err := validateUpsert(good); err != nil {
		t.Errorf("baseline good config rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*UpsertRequest)
	}{
		{"empty host", func(r *UpsertRequest) { r.Host = "" }},
		{"whitespace host", func(r *UpsertRequest) { r.Host = "   " }},
		{"port too low", func(r *UpsertRequest) { r.Port = 0 }},
		{"port too high", func(r *UpsertRequest) { r.Port = 70000 }},
		{"empty from_email", func(r *UpsertRequest) { r.FromEmail = "" }},
		{"malformed from_email", func(r *UpsertRequest) { r.FromEmail = "not-an-email" }},
		{"unsupported encryption", func(r *UpsertRequest) { r.Encryption = "ssl" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := good
			c.mut(&r)
			if err := validateUpsert(r); err == nil {
				t.Errorf("validateUpsert accepted bad config: %+v", r)
			}
		})
	}
}

// TestBuildMIMEMessage_FromHeaderEscaping confirms a display-name with
// embedded quotes / backslashes doesn't corrupt the From header.
// Without escaping, a name like `"Foo " Bar"` would split the header
// and confuse the receiving MTA.
func TestBuildMIMEMessage_FromHeaderEscaping(t *testing.T) {
	sender := &ProjectSender{
		FromEmail: "noreply@example.com",
		FromName:  `Foo "Bar" Baz \ end`,
		Host:      "smtp.example.com",
		Port:      587,
	}
	msg := string(buildMIMEMessage(sender, "to@example.com", "s", "<p>b</p>"))
	if !strings.Contains(msg, `From: "Foo \"Bar\" Baz \\ end" <noreply@example.com>`) {
		t.Errorf("From header not escaped correctly:\n%s", msg)
	}
}

// TestBuildMIMEMessage_BasicShape spot-checks the minimum headers a
// receiving MTA needs to accept the message. If any of these go
// missing, deliverability drops silently.
func TestBuildMIMEMessage_BasicShape(t *testing.T) {
	sender := &ProjectSender{FromEmail: "noreply@example.com"}
	msg := string(buildMIMEMessage(sender, "to@example.com", "Subj", "<p>hi</p>"))
	for _, want := range []string{
		"From: noreply@example.com",
		"To: to@example.com",
		"Subject: Subj",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"Date: ",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in message:\n%s", want, msg)
		}
	}
	// Body separator is a blank line after the headers.
	if !strings.Contains(msg, "\r\n\r\n<p>hi</p>") {
		t.Errorf("body not separated from headers correctly:\n%s", msg)
	}
}
