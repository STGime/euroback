package email

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/netguard"
)

// Rows saved before the host / port / encryption / login rules existed
// are refused at send time, before any lookup or connection.
func TestSendViaCustomSMTP_RefusesBeforeDialing(t *testing.T) {
	var calls int
	orig := smtpDialer
	smtpDialer = &netguard.Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) { calls++; return nil, errors.New("no") },
		Dial:   func(context.Context, string) (net.Conn, error) { calls++; return nil, errors.New("no") },
	}
	defer func() { smtpDialer = orig }()

	good := ProjectSender{Host: "smtp.example.com", Port: 587, Encryption: EncryptionSTARTTLS, Username: "u", Password: "p", FromEmail: "a@example.com"}
	cases := map[string]func(*ProjectSender){
		"internal literal": func(s *ProjectSender) { s.Host = "10.32.0.10" },
		"cluster name":     func(s *ProjectSender) { s.Host = "pgbouncer.eurobase.svc.cluster.local" },
		"port 25":          func(s *ProjectSender) { s.Port = 25 },
		"plaintext":        func(s *ProjectSender) { s.Encryption = EncryptionNone },
		"no password":      func(s *ProjectSender) { s.Password = "" },
		"no username":      func(s *ProjectSender) { s.Username = "" },
	}
	for name, mut := range cases {
		s := good
		mut(&s)
		if err := sendViaCustomSMTP(context.Background(), &s, "x@example.com", "s", "b"); err == nil {
			t.Errorf("%s: sent", name)
		}
	}
	if calls != 0 {
		t.Fatalf("%d lookups / dials for refused configs", calls)
	}
}

// A name that resolves to an internal address: refused, and the error
// names neither that address nor the resolver.
func TestSendViaCustomSMTP_InternalAnswerNotDialed(t *testing.T) {
	dialed := 0
	orig := smtpDialer
	smtpDialer = &netguard.Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.32.0.10")}, nil
		},
		Dial: func(context.Context, string) (net.Conn, error) { dialed++; return nil, errors.New("no") },
	}
	defer func() { smtpDialer = orig }()

	s := ProjectSender{Host: "rebind.example.com", Port: 587, Encryption: EncryptionSTARTTLS, Username: "u", Password: "p", FromEmail: "a@example.com"}
	err := sendViaCustomSMTP(context.Background(), &s, "x@example.com", "s", "b")
	if err == nil || dialed != 0 {
		t.Fatalf("err=%v dialed=%d", err, dialed)
	}
	if strings.Contains(err.Error(), "10.32.0.10") {
		t.Fatalf("error names the internal address: %v", err)
	}
}
