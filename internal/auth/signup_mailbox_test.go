package auth

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The signup handler limits per mailbox ("+tag" and Gmail dots ignored),
// so many addresses on one inbox share one counter, and a refused check
// stops the request before the service is called.
func TestSignUpHandler_LimitsPerMailbox(t *testing.T) {
	var keys []string
	check := func(w http.ResponseWriter, r *http.Request, action, identifier string) bool {
		if action != "platform_signup_mailbox" {
			return false
		}
		keys = append(keys, identifier)
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	}
	h := HandlePlatformSignUp(&PlatformAuthService{}, check) // nil pool: reaching SignUp would panic
	for _, addr := range []string{"Some.One+a@gmail.com", "someone+b@googlemail.com"} {
		body := fmt.Sprintf(`{"email":%q,"password":"a-long-enough-passphrase-42"}`, addr)
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/platform/auth/signup", bytes.NewBufferString(body)))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s: status %d, want 429", addr, rec.Code)
		}
	}
	if len(keys) != 2 || keys[0] != "someone@gmail.com" || keys[1] != "someone@gmail.com" {
		t.Fatalf("mailbox keys = %v, want both someone@gmail.com", keys)
	}
}

// A signup to the mailbox of a suspended account is refused (400, the
// generic "can't be used to sign up"); another mailbox is not.
func TestSuspendedMailbox_DB(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("cannot ping: %v", err)
	}

	suspended := fmt.Sprintf("mb.suspended.%d@gmail.com", os.Getpid())
	cleanupPlatformUser(ctx, pool, suspended)
	t.Cleanup(func() { cleanupPlatformUser(context.Background(), pool, suspended) })
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.platform_users (email, suspended_at) VALUES ($1, now())`, suspended); err != nil {
		t.Skipf("cannot insert a platform user here: %v", err)
	}

	svc := NewPlatformAuthService(pool, "test-jwt-secret-please-ignore")
	same := fmt.Sprintf("mbsuspended%d+again@googlemail.com", os.Getpid())
	if !svc.suspendedMailbox(ctx, same) {
		t.Fatalf("%s shares the suspended account's mailbox; want refused", same)
	}
	_, err = svc.SignUp(ctx, same, "a-long-enough-passphrase-42", nil, "", "")
	if err != ErrBlockedSignupDomain || !isUserError(err) {
		t.Fatalf("SignUp(%s) err = %v, want ErrBlockedSignupDomain (400)", same, err)
	}
	other := fmt.Sprintf("mb.other.%d@gmail.com", os.Getpid())
	if svc.suspendedMailbox(ctx, other) {
		t.Fatalf("%s is another mailbox; want allowed", other)
	}
}
