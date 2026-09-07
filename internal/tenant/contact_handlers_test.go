package tenant

import (
	"strings"
	"testing"
)

func TestValidateContactRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		in           PublicContactRequest
		wantErr      string // substring; empty = expect no error
		wantCanonEml string // if non-empty, assert canonicalised email
	}{
		{
			name:         "happy path — minimal (no name)",
			in:           PublicContactRequest{Email: "user@example.com", Message: "Hi"},
			wantCanonEml: "user@example.com",
		},
		{
			name:         "happy path — with name",
			in:           PublicContactRequest{Name: "Anna", Email: "user@example.com", Message: "Hi"},
			wantCanonEml: "user@example.com",
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
			name:         "multibyte name — 100 runes = 300 bytes should pass",
			in:           PublicContactRequest{Name: strings.Repeat("Ü", 100), Email: "user@example.com", Message: "Hi"},
			wantCanonEml: "user@example.com",
		},
		// ── Regression: mail.ParseAddress accepts display-name form.
		// Without canonicalisation, "Alice <a@b.com>" and "Bob <a@b.com>"
		// hit different rate-limit keys, defeating the per-email cap.
		{
			name:         "display-name form — canonicalised to bare address",
			in:           PublicContactRequest{Email: "Alice <a@b.com>", Message: "Hi"},
			wantCanonEml: "a@b.com",
		},
		{
			name:         "display-name form with rotating name — same canonical",
			in:           PublicContactRequest{Email: "Bob <a@b.com>", Message: "Hi"},
			wantCanonEml: "a@b.com",
		},
		{
			name:         "already-canonical mixed case — lowercased",
			in:           PublicContactRequest{Email: "User@Example.COM", Message: "Hi"},
			wantCanonEml: "user@example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := tt.in
			// Mirror the handler's TrimSpace+ToLower pre-validate
			// normalisation so validator input matches what runs in
			// production.
			in.Email = strings.ToLower(strings.TrimSpace(in.Email))
			in.Message = strings.TrimSpace(in.Message)
			err := validateContactRequest(&in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				if tt.wantCanonEml != "" && in.Email != tt.wantCanonEml {
					t.Fatalf("expected canonical email %q, got %q", tt.wantCanonEml, in.Email)
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

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{name: "shorter than cap — passthrough", s: "hello", n: 10, want: "hello"},
		{name: "exactly at cap — passthrough", s: "hello", n: 5, want: "hello"},
		{name: "byte cut mid-multibyte would be invalid UTF-8", s: strings.Repeat("Ü", 10), n: 3, want: "ÜÜÜ"},
		{name: "unicode 500 runes — cut at 500 runes not bytes", s: strings.Repeat("Ü", 501), n: 500, want: strings.Repeat("Ü", 500)},
		{name: "empty — passthrough", s: "", n: 10, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateRunes(tt.s, tt.n)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
