package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// A signup that fails gives its reserved mailbox slot back, so only
// signups that created an account use up the limit.
func TestSignUpHandler_ReleasesMailboxSlotOnFailure(t *testing.T) {
	var actions []string
	check := func(w http.ResponseWriter, r *http.Request, action, identifier string) bool {
		if action != "platform_signup" {
			actions = append(actions, action+" "+identifier)
		}
		return false
	}
	h := HandlePlatformSignUp(&PlatformAuthService{}, check)
	// A reserved domain fails in SignUp before any database access.
	body := `{"email":"Some.One+a@example.com","password":"a-long-enough-passphrase-42"}`
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/platform/auth/signup", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	want := []string{"platform_signup_mailbox some.one@example.com", "platform_signup_mailbox_release some.one@example.com"}
	if len(actions) != 2 || actions[0] != want[0] || actions[1] != want[1] {
		t.Fatalf("actions = %q, want %q", actions, want)
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
		t.Fatalf("insert suspended platform user: %v", err)
	}

	svc := NewPlatformAuthService(pool, "test-jwt-secret-please-ignore")
	same := fmt.Sprintf("mbsuspended%d+again@googlemail.com", os.Getpid())
	if _, susp := svc.mailboxAccounts(ctx, same); !susp {
		t.Fatalf("%s shares the suspended account's mailbox; want refused", same)
	}
	_, err = svc.SignUp(ctx, same, "a-long-enough-passphrase-42", nil, "", "")
	if !errors.Is(err, ErrMailboxInUse) || !isUserError(err) {
		t.Fatalf("SignUp(%s) err = %v, want ErrMailboxInUse (400, same answer as an active account)", same, err)
	}
	// Refused even when allowlisted.
	if _, err := pool.Exec(ctx, `INSERT INTO platform_allowlist (email) VALUES ($1) ON CONFLICT DO NOTHING`, same); err != nil {
		t.Fatalf("insert allowlist: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform_allowlist WHERE email = $1`, same)
	})
	if _, err := svc.SignUp(ctx, same, "a-long-enough-passphrase-42", nil, "", ""); !errors.Is(err, ErrMailboxInUse) {
		t.Fatalf("SignUp(%s) allowlisted err = %v, want ErrMailboxInUse", same, err)
	}
	// The exact address is left to the insert ("already registered"), so
	// it doesn't reveal the suspension.
	if taken, _ := svc.mailboxAccounts(ctx, strings.ToUpper(suspended)); taken {
		t.Fatalf("exact address %s must not be answered by the mailbox check", suspended)
	}
	other := fmt.Sprintf("mb.other.%d@gmail.com", os.Getpid())
	if taken, _ := svc.mailboxAccounts(ctx, other); taken {
		t.Fatalf("%s is another mailbox; want allowed", other)
	}
}

// One account per inbox: another "+tag" or Gmail spelling of an active
// account's inbox is refused (400, ErrMailboxInUse) unless the address is
// on platform_allowlist; other domains and other local parts are not.
func TestMailboxInUse_DB(t *testing.T) {
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

	pid := os.Getpid()
	existing := fmt.Sprintf("mb.inuse%d@gmail.com", pid)
	plainExisting := fmt.Sprintf("mb.inuse%d@example.org", pid)
	quotedExisting := fmt.Sprintf(`"mbq%d"@gmail.com`, pid)          // legacy quoted spelling
	googlemailExisting := fmt.Sprintf("mb.gm%d@googlemail.com", pid) // stored on googlemail
	allowed := fmt.Sprintf("mbinuse%d+team@gmail.com", pid)
	for _, e := range []string{existing, plainExisting, quotedExisting, googlemailExisting} {
		cleanupPlatformUser(ctx, pool, e)
		if _, err := pool.Exec(ctx, `INSERT INTO public.platform_users (email) VALUES ($1)`, e); err != nil {
			t.Fatalf("insert platform user: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, e := range []string{existing, plainExisting, quotedExisting, googlemailExisting} {
			cleanupPlatformUser(context.Background(), pool, e)
		}
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform_allowlist WHERE email = $1`, allowed)
	})

	svc := NewPlatformAuthService(pool, "test-jwt-secret-please-ignore")
	for _, addr := range []struct {
		email string
		taken bool
	}{
		{fmt.Sprintf("mbinuse%d+euro2@googlemail.com", pid), true},
		{fmt.Sprintf("MB.InUse%d+x@Gmail.com", pid), true},
		{fmt.Sprintf("mb.inuse%d+x@example.org", pid), true},
		{fmt.Sprintf("mbinuse%d@example.org", pid), false}, // dots count outside Gmail
		{fmt.Sprintf("mb.inuse%d@example.net", pid), false},
		{fmt.Sprintf("mb.inuse%d1@gmail.com", pid), false},
		{existing, false}, // the exact address is the insert's answer
		{fmt.Sprintf("mbq%d+2@gmail.com", pid), true},
		{fmt.Sprintf("m.b.q%d@googlemail.com", pid), true},
		{fmt.Sprintf("mbgm%d+x@gmail.com", pid), true},
	} {
		if taken, susp := svc.mailboxAccounts(ctx, addr.email); taken != addr.taken || susp {
			t.Errorf("mailboxAccounts(%s) = %v, %v; want %v, false", addr.email, taken, susp, addr.taken)
		}
	}

	second := fmt.Sprintf("mbinuse%d+euro2@gmail.com", pid)
	if _, err := svc.SignUp(ctx, second, "a-long-enough-passphrase-42", nil, "", ""); !errors.Is(err, ErrMailboxInUse) || !isUserError(err) {
		t.Fatalf("SignUp(%s) err = %v, want ErrMailboxInUse (400)", second, err)
	}
	// An allowlisted address passes this check (and fails later, on the
	// missing acceptances, before anything is written).
	if _, err := pool.Exec(ctx, `INSERT INTO platform_allowlist (email) VALUES ($1) ON CONFLICT DO NOTHING`, allowed); err != nil {
		t.Fatalf("insert allowlist: %v", err)
	}
	if _, err := svc.SignUp(ctx, allowed, "a-long-enough-passphrase-42", nil, "", ""); err == nil ||
		!strings.HasPrefix(err.Error(), "you must accept the following to sign up:") {
		t.Fatalf("SignUp(%s) err = %v, want the acceptances error (past the inbox check)", allowed, err)
	}
}
