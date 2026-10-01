package tenant

import (
	"context"
	"crypto/rand"
	"os"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The account half of the superadmin SSO bypass, on the developer login
// when CI provides it (platform_passkey_credentials is revoked from the
// gateway role): still superadmin AND a passkey older than a day.
func TestSuperadminBypassVerify(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	devPool := pool
	if u := os.Getenv("PAT_TEST_DEVELOPER_URL"); u != "" {
		p, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		devPool = p
	}
	user := insertTestPlatformUser(t, pool, "sa-bypass@test.eurobase.local")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM platform_users WHERE id = $1`, user) })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	cred := make([]byte, 16)
	_, _ = rand.Read(cred)

	if auth.VerifySuperadminPasskeyBypass(ctx, devPool, user) {
		t.Fatal("not a superadmin, no passkey: want false")
	}
	exec(`UPDATE platform_users SET is_superadmin = true WHERE id = $1`, user)
	if auth.VerifySuperadminPasskeyBypass(ctx, devPool, user) {
		t.Fatal("superadmin without a passkey: want false")
	}
	exec(`INSERT INTO platform_passkey_credentials (platform_user_id, credential_id, public_key) VALUES ($1, $2, '\x00')`, user, cred)
	if auth.VerifySuperadminPasskeyBypass(ctx, devPool, user) {
		t.Fatal("passkey registered just now: want false")
	}
	exec(`UPDATE platform_passkey_credentials SET created_at = now() - interval '2 days' WHERE platform_user_id = $1`, user)
	if !auth.VerifySuperadminPasskeyBypass(ctx, devPool, user) {
		t.Fatal("superadmin with a 2-day-old passkey: want true")
	}
	exec(`UPDATE platform_users SET is_superadmin = false WHERE id = $1`, user)
	if auth.VerifySuperadminPasskeyBypass(ctx, devPool, user) {
		t.Fatal("superadmin flag revoked: want false")
	}
}
