package tenant

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateContactRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      PublicContactRequest
		wantErr string // substring; empty = expect no error
	}{
		{
			name: "happy path — minimal (no name)",
			in:   PublicContactRequest{Email: "user@example.com", Message: "Hi"},
		},
		{
			name: "happy path — with name",
			in:   PublicContactRequest{Name: "Anna", Email: "user@example.com", Message: "Hi"},
		},
		{
			name:    "missing email",
			in:      PublicContactRequest{Message: "Hi"},
			wantErr: "email is required",
		},
		{
			name:    "invalid email",
			in:      PublicContactRequest{Email: "not-an-email", Message: "Hi"},
			wantErr: "not a valid address",
		},
		{
			name:    "missing message",
			in:      PublicContactRequest{Email: "user@example.com"},
			wantErr: "message is required",
		},
		{
			name:    "message too long",
			in:      PublicContactRequest{Email: "user@example.com", Message: strings.Repeat("x", contactMaxMessageLen+1)},
			wantErr: "5000 characters",
		},
		{
			name:    "name too long",
			in:      PublicContactRequest{Name: strings.Repeat("x", contactMaxNameLen+1), Email: "user@example.com", Message: "Hi"},
			wantErr: "200 characters",
		},
		{
			name: "multibyte name — 100 runes = 300 bytes should pass",
			in:   PublicContactRequest{Name: strings.Repeat("Ü", 100), Email: "user@example.com", Message: "Hi"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := tt.in
			err := validateContactRequest(&in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestExtractClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		xff        string
		remoteAddr string
		want       string
	}{
		{
			name:       "no XFF — falls back to RemoteAddr",
			remoteAddr: "203.0.113.42:54321",
			want:       "203.0.113.42",
		},
		{
			name:       "XFF single IP — used",
			xff:        "203.0.113.42",
			remoteAddr: "10.0.0.1:8080",
			want:       "203.0.113.42",
		},
		{
			name:       "XFF list — first parseable used",
			xff:        "203.0.113.42, 10.0.0.1, 172.16.0.1",
			remoteAddr: "10.0.0.1:8080",
			want:       "203.0.113.42",
		},
		{
			name:       "XFF with a garbage leading entry — skipped",
			xff:        "not-an-ip, 198.51.100.7",
			remoteAddr: "10.0.0.1:8080",
			want:       "198.51.100.7",
		},
		{
			name:       "RemoteAddr without port — passthrough",
			remoteAddr: "203.0.113.42",
			want:       "203.0.113.42",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("POST", "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			got := extractClientIP(r)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
