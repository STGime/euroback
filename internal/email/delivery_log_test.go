package email

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/netguard"
)

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"petra@perweg.de":  "p****@perweg.de",
		"  P@x.io ":        "P****@x.io",
		"ännä@example.com": "ä****@example.com",
		"@example.com":     "****@example.com",
		"no-at-sign":       "****",
		"a@b@c.example":    "a****@c.example",
	} {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateKeepsRunes(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := truncate(s, 5)
	if !strings.HasSuffix(got, "…") || strings.ContainsRune(got, '�') {
		t.Fatalf("truncate cut a rune: %q", got)
	}
	if truncate("short", 10) != "short" {
		t.Fatal("short strings must be unchanged")
	}
}

func TestClassifySend(t *testing.T) {
	smtpErr := &SMTPError{Stage: StageAuth, Addr: "mail.example.com:587", Err: errors.New("535 bad credentials")}
	cases := []struct {
		name                     string
		d                        Delivery
		err                      error
		outcome, reason, via, id string
		detailHas                string
	}{
		{"platform sent", Delivery{Via: ViaPlatform, MessageID: "tem-1"}, nil, OutcomeSent, "", ViaPlatform, "tem-1", ""},
		{"custom sent", Delivery{Via: ViaCustomSMTP}, nil, OutcomeSent, "", ViaCustomSMTP, "", ""},
		{"not configured", Delivery{}, nil, OutcomeSkipped, ReasonEmailNotConfigured, "", "", ""},
		{"custom failed", Delivery{Via: ViaCustomSMTP}, smtpErr, OutcomeFailed, ReasonCustomSMTPFailed, ViaCustomSMTP, "", "rejected the login"},
		{"platform failed", Delivery{Via: ViaPlatform}, &TEMError{Status: 429, Body: `{"internal":"do not show"}`}, OutcomeFailed, ReasonPlatformFailed, ViaPlatform, "", "HTTP 429"},
		{"prepare failed", Delivery{}, errors.New("store magic link token: boom"), OutcomeFailed, ReasonInternal, "", "", "couldn't be prepared"},
	}
	for _, c := range cases {
		e := classifySend(FlowMagicLink, "p@x.io", c.d, c.err)
		if e.Outcome != c.outcome || e.Reason != c.reason || e.Via != c.via || e.MessageID != c.id {
			t.Errorf("%s: got %+v", c.name, e)
		}
		if c.detailHas != "" && !strings.Contains(e.Detail, c.detailHas) {
			t.Errorf("%s: detail %q lacks %q", c.name, e.Detail, c.detailHas)
		}
		if strings.Contains(e.Detail, "do not show") || strings.Contains(e.Detail, "boom") {
			t.Errorf("%s: internal error text leaked into the project's log: %q", c.name, e.Detail)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestExplainSMTPError(t *testing.T) {
	defer SetSMTPEgressBlocked(false)

	dialTimeout := &SMTPError{Stage: StageDial, Addr: "mail.perweg.de:587", Encryption: EncryptionSTARTTLS, Err: &net.OpError{Op: "dial", Err: timeoutErr{}}}
	SetSMTPEgressBlocked(false)
	if h := ExplainSMTPError(dialTimeout); !strings.Contains(h, "No answer from mail.perweg.de:587") || strings.Contains(h, "hosting provider") {
		t.Errorf("timeout, not blocked: %q", h)
	}
	SetSMTPEgressBlocked(true)
	if h := ExplainSMTPError(dialTimeout); !strings.Contains(h, "hosting provider currently blocks") {
		t.Errorf("timeout, blocked: %q", h)
	}
	SetSMTPEgressBlocked(false)

	for _, c := range []struct {
		err  error
		want string
	}{
		{&SMTPError{Stage: StageDial, Addr: "nope.invalid:587", Err: &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}}, "host name wasn't found"},
		{&SMTPError{Stage: StageGreeting, Addr: "h:465", Encryption: EncryptionSTARTTLS, Err: timeoutErr{}}, "Port 465 expects TLS"},
		{&SMTPError{Stage: StageAuth, Addr: "h:25", Err: errors.New("unencrypted connection")}, "Choose STARTTLS or TLS"},
		{&SMTPError{Stage: StageMailFrom, Addr: "h:587", Err: errors.New("553 not allowed")}, "From address"},
		{&SMTPError{Stage: StageRcptTo, Addr: "h:587", Err: errors.New("550 no")}, "recipient"},
	} {
		if h := ExplainSMTPError(c.err); !strings.Contains(h, c.want) {
			t.Errorf("%v: explanation %q lacks %q", c.err, h, c.want)
		}
	}
	if ExplainSMTPError(errors.New("plain")) != "" {
		t.Error("non-SMTP errors get no explanation")
	}
}

// routeSMTPTo sends every custom-SMTP connection to a local address (the
// real dialer only connects to public ones): the name resolves to a
// public address, and the dial goes to local.
func routeSMTPTo(t *testing.T, local string) {
	t.Helper()
	orig := smtpDialer
	smtpDialer = &netguard.Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("51.15.217.235")}, nil
		},
		Dial: func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", local)
		},
	}
	t.Cleanup(func() { smtpDialer = orig })
}

// Real sockets: a refused port, TLS against a plaintext SMTP server, and a
// host that isn't public.
func TestExplainSMTPError_RealDials(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedAddr := ln.Addr().String()
	ln.Close()

	routeSMTPTo(t, refusedAddr)
	sender := &ProjectSender{Host: "smtp.provider.example.eu", Port: 587, Encryption: EncryptionSTARTTLS, Username: "u", Password: "p", FromEmail: "a@b.c"}
	err = sendViaCustomSMTP(context.Background(), sender, "x@y.z", "s", "b")
	if h := ExplainSMTPError(err); !strings.Contains(h, "refused the connection on port 587") {
		t.Errorf("refused: %v → %q", err, h)
	}
	if !strings.HasPrefix(err.Error(), "dial smtp.provider.example.eu:587: ") {
		t.Errorf("error text changed: %q", err.Error())
	}

	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	go func() {
		for {
			c, err := plain.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("220 plain smtp ready\r\n"))
			time.Sleep(200 * time.Millisecond)
			c.Close()
		}
	}()
	routeSMTPTo(t, plain.Addr().String())
	sender = &ProjectSender{Host: "smtp.provider.example.eu", Port: 465, Encryption: EncryptionTLS, Username: "u", Password: "p", FromEmail: "a@b.c"}
	err = sendViaCustomSMTP(context.Background(), sender, "x@y.z", "s", "b")
	var rec tls.RecordHeaderError
	if !errors.As(err, &rec) {
		t.Fatalf("expected a TLS record header error, got %v", err)
	}
	if h := ExplainSMTPError(err); !strings.Contains(h, "doesn't use TLS from the start") {
		t.Errorf("tls on plaintext: %q", h)
	}

	// A name that resolves only to an internal address.
	orig := smtpDialer
	smtpDialer = &netguard.Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.32.0.10")}, nil
	}}
	defer func() { smtpDialer = orig }()
	sender.Port = 587
	sender.Encryption = EncryptionSTARTTLS
	err = sendViaCustomSMTP(context.Background(), sender, "x@y.z", "s", "b")
	if d := DescribeSMTPError(err); !strings.Contains(d, "public DNS") || strings.Contains(d, "10.32.0.10") {
		t.Errorf("not public: %q", d)
	}
}

func atoiMust(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
