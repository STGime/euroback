package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeEmailer implements PlatformEmailer against the real pool: it
// stores/consumes verification tokens the same way EmailService does
// (SHA-256 hash, single-use), so VerifyEmail exercises the real token
// round-trip while the test can recover the raw token it "sent".
type fakeEmailer struct {
	pool    *pgxpool.Pool
	lastRaw string
	sends   int
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (f *fakeEmailer) SendPlatformVerificationEmail(ctx context.Context, userID, _ string) error {
	f.sends++
	f.lastRaw = "verif-token-" + userID + fmt.Sprintf("-%d", f.sends)
	_, err := f.pool.Exec(ctx,
		`INSERT INTO public.platform_email_tokens (user_id, token_hash, token_type, expires_at)
		 VALUES ($1, $2, 'verification', now() + interval '24 hours')`,
		userID, hashHex(f.lastRaw))
	return err
}

func (f *fakeEmailer) SendPlatformPasswordResetEmail(ctx context.Context, userID, email string) error {
	return nil
}

func (f *fakeEmailer) VerifyPlatformToken(ctx context.Context, rawToken, tokenType string) (string, error) {
	var uid string
	err := f.pool.QueryRow(ctx,
		`UPDATE public.platform_email_tokens SET used_at = now()
		  WHERE token_hash = $1 AND token_type = $2 AND expires_at > now() AND used_at IS NULL
		  RETURNING user_id`,
		hashHex(rawToken), tokenType).Scan(&uid)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("invalid or expired token")
	}
	return uid, err
}

func TestPlatformSignupVerificationFlow(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping platform verification integration test")
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

	// Need active terms + dpa legal_documents rows to sign up. Use
	// whatever the DB has seeded; skip if the seed isn't present.
	accepted, ok := activeLegalDocs(ctx, t, pool)
	if !ok {
		t.Skip("no active terms/dpa legal_documents seeded; skipping")
	}

	email := fmt.Sprintf("verif-flow-%d@eurobase.test", os.Getpid())
	t.Cleanup(func() { cleanupPlatformUser(context.Background(), pool, email) })
	cleanupPlatformUser(ctx, pool, email) // clear any residue from a crashed run

	fake := &fakeEmailer{pool: pool}
	svc := NewPlatformAuthService(pool, "test-jwt-secret-please-ignore")
	svc.SetEmailService(fake)
	svc.AllowPublicSignup = true

	const strongPW = "correct-horse-battery-staple-42"

	// 1. Signup → no session, verification required, email unconfirmed.
	resp, err := svc.SignUp(ctx, email, strongPW, accepted, "", "")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if !resp.EmailVerificationRequired {
		t.Error("expected EmailVerificationRequired=true")
	}
	if resp.AccessToken != "" {
		t.Error("expected no access token before verification")
	}
	if fake.sends != 1 {
		t.Errorf("expected 1 verification send, got %d", fake.sends)
	}
	if confirmed := emailConfirmed(ctx, t, pool, email); confirmed {
		t.Error("email_confirmed_at should be NULL right after signup")
	}

	// 2. Weak password is rejected (does not create a user).
	_, werr := svc.SignUp(ctx, "weak-"+email, "password1234", accepted, "", "")
	var weak *ErrWeakPassword
	if !errors.As(werr, &weak) {
		t.Errorf("expected *ErrWeakPassword for weak signup, got %v", werr)
	}

	// 3. Sign-in is hard-blocked until verified.
	_, serr := svc.SignIn(ctx, email, strongPW)
	if !errors.Is(serr, ErrEmailNotVerified) {
		t.Fatalf("expected ErrEmailNotVerified, got %v", serr)
	}

	// 4. Verify with the emailed token → confirms + issues a session.
	vresp, err := svc.VerifyEmail(ctx, fake.lastRaw)
	if err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}
	if vresp.AccessToken == "" {
		t.Error("expected a session token after verification")
	}
	if !emailConfirmed(ctx, t, pool, email) {
		t.Error("email_confirmed_at should be set after verification")
	}

	// 5. The token is single-use.
	if _, err := svc.VerifyEmail(ctx, fake.lastRaw); err == nil {
		t.Error("expected re-use of a verification token to fail")
	}

	// 6. Sign-in now succeeds.
	si, err := svc.SignIn(ctx, email, strongPW)
	if err != nil {
		t.Fatalf("SignIn after verify: %v", err)
	}
	if si.AccessToken == "" {
		t.Error("expected a session token on verified sign-in")
	}

	// 7. Resend for an already-verified user sends nothing.
	before := fake.sends
	if err := svc.ResendVerification(ctx, email); err != nil {
		t.Errorf("ResendVerification returned error: %v", err)
	}
	if fake.sends != before {
		t.Error("resend should not send for an already-verified account")
	}

	// 8. ChangePassword enforces the same strength policy.
	if err := svc.ChangePassword(ctx, si.User.ID, strongPW, "password1234"); err == nil {
		t.Error("ChangePassword should reject a weak new password")
	} else {
		var weakChange *ErrWeakPassword
		if !errors.As(err, &weakChange) {
			t.Errorf("expected *ErrWeakPassword from ChangePassword, got %v", err)
		}
	}
	if err := svc.ChangePassword(ctx, si.User.ID, strongPW, "a-different-strong-passphrase-9"); err != nil {
		t.Errorf("ChangePassword should accept a strong new password: %v", err)
	}
}

func activeLegalDocs(ctx context.Context, t *testing.T, pool *pgxpool.Pool) ([]AcceptedDocument, bool) {
	t.Helper()
	out := []AcceptedDocument{}
	for _, dt := range []string{"terms", "dpa"} {
		var v string
		err := pool.QueryRow(ctx,
			`SELECT version FROM legal_documents
			  WHERE document_type = $1 AND active = true AND superseded_at IS NULL
			  ORDER BY effective_at DESC LIMIT 1`, dt).Scan(&v)
		if err != nil {
			return nil, false
		}
		out = append(out, AcceptedDocument{Type: dt, Version: v})
	}
	return out, true
}

func emailConfirmed(ctx context.Context, t *testing.T, pool *pgxpool.Pool, email string) bool {
	t.Helper()
	var confirmed bool
	err := pool.QueryRow(ctx,
		`SELECT email_confirmed_at IS NOT NULL FROM platform_users WHERE email = $1`, email).Scan(&confirmed)
	if err != nil {
		t.Fatalf("emailConfirmed query: %v", err)
	}
	return confirmed
}

func cleanupPlatformUser(ctx context.Context, pool *pgxpool.Pool, email string) {
	var id string
	if err := pool.QueryRow(ctx, `SELECT id FROM platform_users WHERE email = $1`, email).Scan(&id); err == nil {
		_, _ = pool.Exec(ctx, `DELETE FROM legal_acceptances WHERE user_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_email_tokens WHERE user_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE id = $1`, id)
	}
}

// TestDeleteAccount_RefusesSoleAdminOfSharedOrg pins the self-delete
// mirror of AdminDeleteUser's sole-admin-with-other-members guard.
// Sole admin of an org that still has other (non-admin) members must
// hand off before self-deletion — returning the ErrOrgHandoffRequired
// sentinel so the HTTP handler can map it to 409 rather than 500.
func TestDeleteAccount_RefusesSoleAdminOfSharedOrg(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping self-delete org integration test")
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

	adminEmail := fmt.Sprintf("selfdel-admin-%d@eurobase.test", os.Getpid())
	memberEmail := fmt.Sprintf("selfdel-member-%d@eurobase.test", os.Getpid())
	t.Cleanup(func() {
		cleanupPlatformUser(context.Background(), pool, adminEmail)
		cleanupPlatformUser(context.Background(), pool, memberEmail)
	})
	cleanupPlatformUser(ctx, pool, adminEmail)
	cleanupPlatformUser(ctx, pool, memberEmail)

	var adminID, memberID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (email, password_hash, email_confirmed_at)
		 VALUES ($1, 'x', now()) RETURNING id::text`, adminEmail).Scan(&adminID); err != nil {
		t.Fatalf("insert admin user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (email, password_hash, email_confirmed_at)
		 VALUES ($1, 'x', now()) RETURNING id::text`, memberEmail).Scan(&memberID); err != nil {
		t.Fatalf("insert member user: %v", err)
	}

	var orgID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.organizations (name) VALUES ($1) RETURNING id::text`,
		"selfdel fixture org",
	).Scan(&orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.organizations WHERE id = $1::uuid`, orgID)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'admin', 'manual')`,
		orgID, adminID); err != nil {
		t.Fatalf("insert admin membership: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'member', 'manual')`,
		orgID, memberID); err != nil {
		t.Fatalf("insert plain membership: %v", err)
	}

	svc := NewPlatformAuthService(pool, "test-jwt-secret-please-ignore")
	svc.SetDeveloperPool(pool) // same pool works in tests; prod splits them

	err = svc.DeleteAccount(ctx, adminID)
	if !errors.Is(err, ErrOrgHandoffRequired) {
		t.Fatalf("DeleteAccount: got %v, want ErrOrgHandoffRequired", err)
	}
	// Admin row still present.
	var still bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM platform_users WHERE id = $1::uuid)`, adminID).Scan(&still); err != nil {
		t.Fatalf("check admin row: %v", err)
	}
	if !still {
		t.Errorf("admin row should NOT have been deleted")
	}
}

// TestDeleteAccount_DeletesSoloOrg pins the self-delete happy path
// mirror of AdminDeleteUser: caller is the ONLY member of an org,
// account-delete succeeds AND cleans up the now-empty org.
func TestDeleteAccount_DeletesSoloOrg(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping self-delete solo-org test")
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

	email := fmt.Sprintf("selfdel-solo-%d@eurobase.test", os.Getpid())
	t.Cleanup(func() { cleanupPlatformUser(context.Background(), pool, email) })
	cleanupPlatformUser(ctx, pool, email)

	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (email, password_hash, email_confirmed_at)
		 VALUES ($1, 'x', now()) RETURNING id::text`, email).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var orgID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.organizations (name) VALUES ($1) RETURNING id::text`,
		"selfdel solo fixture",
	).Scan(&orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.organizations WHERE id = $1::uuid`, orgID)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
		 VALUES ($1::uuid, $2::uuid, 'admin', 'manual')`,
		orgID, userID); err != nil {
		t.Fatalf("insert solo membership: %v", err)
	}

	svc := NewPlatformAuthService(pool, "test-jwt-secret-please-ignore")
	svc.SetDeveloperPool(pool)

	if err := svc.DeleteAccount(ctx, userID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	var userStill, orgStill bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM platform_users WHERE id = $1::uuid)`, userID).Scan(&userStill); err != nil {
		t.Fatalf("check user row: %v", err)
	}
	if userStill {
		t.Errorf("user row should have been deleted")
	}
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM public.organizations WHERE id = $1::uuid)`, orgID).Scan(&orgStill); err != nil {
		t.Fatalf("check org row: %v", err)
	}
	if orgStill {
		t.Errorf("solo org row should have been deleted alongside the user")
	}
}
