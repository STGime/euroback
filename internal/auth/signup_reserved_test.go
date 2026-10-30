package auth

import (
	"context"
	"errors"
	"testing"
)

// Signups with addresses on reserved test domains are refused up front
// (they can never receive the verification email) as a user error (400).
func TestSignUp_RefusesReservedAddress(t *testing.T) {
	svc := &PlatformAuthService{}
	for _, addr := range []string{"someone@example.com", "x@mail.example.org", "probe@app.test"} {
		_, err := svc.SignUp(context.Background(), addr, "a-long-enough-passphrase-42", nil, "", "")
		if !errors.Is(err, ErrUndeliverableEmail) {
			t.Fatalf("%s: err = %v, want ErrUndeliverableEmail", addr, err)
		}
		if !isUserError(err) {
			t.Fatalf("%s: must map to 400", addr)
		}
	}
}
