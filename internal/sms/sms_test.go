package sms

import (
	"testing"
)

func TestParseMSISDN(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"+33612345678", 33612345678, false},
		{"+4917612345678", 4917612345678, false},
		{"+1234567890", 1234567890, false},
		{"33612345678", 0, true},  // missing +
		{"+123", 0, true},         // too short
		{"+123abc456", 0, true},   // non-digits
		{"", 0, true},             // empty
	}

	for _, tt := range tests {
		result, err := parseMSISDN(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseMSISDN(%q) expected error, got %d", tt.input, result)
			}
		} else {
			if err != nil {
				t.Errorf("parseMSISDN(%q) unexpected error: %v", tt.input, err)
			} else if result != tt.expected {
				t.Errorf("parseMSISDN(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		}
	}
}

func TestGenerateOTPCode(t *testing.T) {
	code, err := generateOTPCode()
	if err != nil {
		t.Fatalf("generateOTPCode() error: %v", err)
	}
	if len(code) != 6 {
		t.Errorf("OTP code length = %d, want 6", len(code))
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			t.Errorf("OTP code contains non-digit: %c", c)
		}
	}

	// Generate multiple codes to check they're not all the same.
	codes := make(map[string]bool)
	for i := 0; i < 10; i++ {
		c, _ := generateOTPCode()
		codes[c] = true
	}
	if len(codes) < 2 {
		t.Error("OTP codes appear non-random (all identical)")
	}
}

func TestClientConfigured(t *testing.T) {
	c := NewClient("", "")
	if c.Configured() {
		t.Error("empty client should not be configured")
	}

	c = NewClient("test-token", "")
	if !c.Configured() {
		t.Error("client with token should be configured")
	}
}

func TestSenderTruncation(t *testing.T) {
	c := NewClient("token", "VeryLongSenderName")
	if len(c.sender) > 11 {
		t.Errorf("sender should be truncated to 11 chars, got %d", len(c.sender))
	}
}
