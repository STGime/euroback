package compliance

import "testing"

// #353 (+ #356 review): validateEndpoint per-kind rules matter
// because a mis-typed webhook URL or plaintext http would silently
// open an insecure forwarding path, and permissive host validation
// would let a tenant point the deliverers at internal targets
// (SSRF-via-integration — cloud metadata service, cluster-internal
// IPs, k8s API, shared Postgres). Every rejection reason pinned
// with a substring so a future edit can't quietly relax it.
func TestValidateEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		kind    DestinationKind
		ep      string
		wantErr string // substring the error must contain; "" = accept
	}{
		// Webhook accept
		{"webhook https accepted", DestinationWebhook, "https://example.com/audit", ""},
		// Webhook scheme / shape rejections
		{"webhook http rejected", DestinationWebhook, "http://example.com/audit", "https"},
		{"webhook empty rejected", DestinationWebhook, "", "required"},
		{"webhook whitespace rejected", DestinationWebhook, "   ", "required"},
		{"webhook missing host rejected", DestinationWebhook, "https://", "host"},
		{"webhook bad scheme rejected", DestinationWebhook, "ftp://example.com", "https"},
		// Webhook SSRF rejections
		{"webhook localhost rejected", DestinationWebhook, "https://localhost/audit", "localhost"},
		{"webhook 127.0.0.1 rejected", DestinationWebhook, "https://127.0.0.1/audit", "loopback"},
		{"webhook RFC1918 10.x rejected", DestinationWebhook, "https://10.0.0.1/audit", "private"},
		{"webhook RFC1918 192.168 rejected", DestinationWebhook, "https://192.168.1.1/audit", "private"},
		{"webhook RFC1918 172.16 rejected", DestinationWebhook, "https://172.16.0.1/audit", "private"},
		{"webhook AWS metadata IP rejected", DestinationWebhook, "https://169.254.169.254/latest/meta-data/", "link-local"},
		{"webhook IPv6 loopback rejected", DestinationWebhook, "https://[::1]/audit", "loopback"},
		{"webhook IPv6 ULA rejected", DestinationWebhook, "https://[fc00::1]/audit", "private"},
		{"webhook 0.0.0.0 rejected", DestinationWebhook, "https://0.0.0.0/audit", "0.0.0.0"},
		{"webhook IPv4-mapped IPv6 rejected", DestinationWebhook, "https://[::ffff:10.0.0.1]/audit", "private"},
		{"webhook CGNAT rejected", DestinationWebhook, "https://100.64.0.1/audit", "CGNAT"},
		{"webhook CGNAT upper boundary rejected", DestinationWebhook, "https://100.127.255.255/audit", "CGNAT"},

		// Syslog accept
		{"syslog host:port accepted", DestinationSyslog, "siem.example.com:6514", ""},
		// Syslog SSRF rejections
		{"syslog RFC1918 rejected", DestinationSyslog, "10.0.0.1:6514", "private"},
		{"syslog localhost rejected", DestinationSyslog, "localhost:6514", "localhost"},
		{"syslog 127.0.0.1 rejected", DestinationSyslog, "127.0.0.1:6514", "loopback"},
		{"syslog metadata IP rejected", DestinationSyslog, "169.254.169.254:80", "link-local"},
		// Syslog shape rejections
		{"syslog missing port rejected", DestinationSyslog, "siem.example.com", "host:port"},
		{"syslog empty rejected", DestinationSyslog, "", "required"},
		{"syslog trailing garbage rejected", DestinationSyslog, "siem.example.com:6514/junk", "1..65535"},
		{"syslog port out of range rejected", DestinationSyslog, "siem.example.com:99999", "1..65535"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEndpoint(tc.kind, tc.ep)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected accept, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !containsFold(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

// Kind / format are DB-CHECK-constrained; the validator layer
// catches typos before the round-trip. Pin the closed set.
func TestValidateKindAndFormat(t *testing.T) {
	for _, k := range []DestinationKind{DestinationWebhook, DestinationSyslog} {
		if err := validateKind(k); err != nil {
			t.Errorf("valid kind %q rejected: %v", k, err)
		}
	}
	for _, k := range []DestinationKind{"", "email", "kafka"} {
		if err := validateKind(k); err == nil {
			t.Errorf("invalid kind %q accepted", k)
		}
	}
	for _, f := range []DestinationFormat{FormatJSON, FormatCEF} {
		if err := validateFormat(f); err != nil {
			t.Errorf("valid format %q rejected: %v", f, err)
		}
	}
	for _, f := range []DestinationFormat{"", "xml", "protobuf"} {
		if err := validateFormat(f); err == nil {
			t.Errorf("invalid format %q accepted", f)
		}
	}
}

// containsFold is defined in the compliance package elsewhere?
// Local copy for this test file — simple and self-contained.
func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	sl := []byte(toLowerASCII(s))
	tl := []byte(toLowerASCII(sub))
	for i := 0; i+len(tl) <= len(sl); i++ {
		match := true
		for j := 0; j < len(tl); j++ {
			if sl[i+j] != tl[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func toLowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
