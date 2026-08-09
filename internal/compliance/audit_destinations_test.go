package compliance

import "testing"

// #353: validateEndpoint per-kind rules matter because a mis-typed
// webhook URL or plaintext http would silently open an insecure
// forwarding path. Pin the each rejection reason so a future edit
// can't quietly relax it.
func TestValidateEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		kind    DestinationKind
		ep      string
		wantErr string // substring the error must contain; "" = accept
	}{
		{"webhook https accepted", DestinationWebhook, "https://example.com/audit", ""},
		{"webhook http rejected", DestinationWebhook, "http://example.com/audit", "https"},
		{"webhook empty rejected", DestinationWebhook, "", "required"},
		{"webhook whitespace rejected", DestinationWebhook, "   ", "required"},
		{"webhook missing host rejected", DestinationWebhook, "https://", "host"},
		{"webhook bad scheme rejected", DestinationWebhook, "ftp://example.com", "https"},

		{"syslog host:port accepted", DestinationSyslog, "siem.example.com:6514", ""},
		{"syslog IP:port accepted", DestinationSyslog, "10.0.0.1:6514", ""},
		{"syslog missing port rejected", DestinationSyslog, "siem.example.com", "host:port"},
		{"syslog empty rejected", DestinationSyslog, "", "required"},
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
