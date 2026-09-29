package email

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

// Rows saved before the host / port / encryption / login rules existed
// are refused at send time, before any connection is made.
func TestSendViaCustomSMTP_RefusesBeforeDialing(t *testing.T) {
	var dials atomic.Int32
	orig := dialSMTP
	dialSMTP = func(ctx context.Context, d *net.Dialer, host, port string) (net.Conn, error) {
		dials.Add(1)
		return orig(ctx, d, host, port)
	}
	defer func() { dialSMTP = orig }()

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
		err := sendViaCustomSMTP(context.Background(), &s, "x@example.com", "s", "b")
		if err == nil {
			t.Errorf("%s: sent", name)
			continue
		}
		if strings.Contains(err.Error(), "10.32.0.10") && name != "internal literal" {
			t.Errorf("%s: error names an address: %v", name, err)
		}
	}
	if n := dials.Load(); n != 0 {
		t.Fatalf("dialed %d times for refused configs", n)
	}
}
