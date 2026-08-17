package sms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	edb "github.com/eurobase/euroback/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Real-Postgres integration coverage for the #233 phone-OTP fix.
// The unit test (`TestMaxOTPAttempts`) pins the constant but doesn't
// exercise the SQL — the concurrency behaviour, phone-binding, and
// exhaustion path are all only visible against a live DB.
//
// The #420 review 🟡 called this out: the harness for real-PG tests
// already exists (see internal/tenant/service_test.go and
// internal/dbprovider/rls_isolation_dedicated_test.go), so there's
// no reason to defer to staging.
//
// Skips cleanly when DATABASE_URL isn't reachable, so `go test
// ./...` in an environment without Postgres stays green.

// setupTestPool connects to the local test database using the same
// pattern as internal/tenant/service_test.go. Returns nil after
// t.Skip if no DB is available.
func setupTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}
	// Bail out if migration 000104 hasn't been applied (schema
	// prerequisite for this whole test). A "no rows / attempts
	// missing" failure would be inscrutable otherwise.
	var attemptsExists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_name = 'email_tokens' AND column_name = 'attempts'
		)`).Scan(&attemptsExists)
	if err != nil || !attemptsExists {
		pool.Close()
		t.Skip("migration 000104 not applied on target DB (attempts column missing) — run `make migrate`")
	}
	t.Cleanup(pool.Close)
	return pool
}

// makeTestProject provisions a fresh tenant schema via provision_tenant
// and returns (projectID, schemaName). Caller is expected to defer the
// returned cleanup func.
func makeTestProject(t *testing.T, pool *pgxpool.Pool) (string, string, func()) {
	t.Helper()
	ctx := context.Background()

	var b [16]byte
	_, _ = rand.Read(b[:])
	h := hex.EncodeToString(b[:])
	projectID := fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
	slug := "otptest-" + h[0:8]
	// Fresh platform_user + project row so provision_tenant can hook
	// into the projects.schema_name update at the end. Owner-less
	// projects would fail FK; hence a throwaway user.
	var ownerID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO platform_users (email, password_hash, email_confirmed_at)
		VALUES ($1, 'x', now())
		RETURNING id::text`, "otptest-"+h[0:8]+"@test.eurobase.local").Scan(&ownerID); err != nil {
		t.Fatalf("insert throwaway owner: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, owner_id, name, slug, region, plan, status)
		VALUES ($1, $2, $3, $4, 'fr-par', 'free', 'active')`,
		projectID, ownerID, "otptest", slug); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1::uuid, $2, 'free')`, projectID, "otptest"); err != nil {
		t.Fatalf("provision_tenant: %v", err)
	}
	schemaName := "tenant_" + hex.EncodeToString(b[:])
	// provision_tenant replaces '-' with '_' but works on the raw
	// UUID; recompute canonically to match.
	schemaName = "tenant_" + h[0:8] + "_" + h[8:12] + "_" + h[12:16] + "_" + h[16:20] + "_" + h[20:32]

	cleanup := func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `SELECT deprovision_tenant($1::uuid)`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE id = $1`, ownerID)
	}
	return projectID, schemaName, cleanup
}

// insertPhoneUser creates a users row with `phone` set and returns
// its id. Runs via RunAsAuthService so the intent GUC lets the
// insert past RLS.
func insertPhoneUser(t *testing.T, pool *pgxpool.Pool, schema, phone string) string {
	t.Helper()
	var userID string
	err := edb.RunAsAuthService(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			fmt.Sprintf(`INSERT INTO %q.users (phone) VALUES ($1) RETURNING id::text`, schema),
			phone,
		).Scan(&userID)
	})
	if err != nil {
		t.Fatalf("insert phone user: %v", err)
	}
	return userID
}

// seedOTP inserts an email_tokens row for the given user with a
// fresh known code. Returns the plaintext code so the test can
// verify (or intentionally not verify). Bypasses the send-SMS
// path entirely — we're testing verify semantics, not the send
// pipeline.
func seedOTP(t *testing.T, pool *pgxpool.Pool, schema, userID string) string {
	t.Helper()
	code, err := generateOTPCode()
	if err != nil {
		t.Fatalf("generateOTPCode: %v", err)
	}
	err = edb.RunAsAuthService(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %q.email_tokens (user_id, token_hash, token_type, expires_at)
			             VALUES ($1, $2, 'phone_verification', now() + interval '10 minutes')`, schema),
			userID, hashSHA256(code),
		)
		return e
	})
	if err != nil {
		t.Fatalf("seed otp: %v", err)
	}
	return code
}

// TestVerifyOTP_Integration is the P1 fix's behavioural test — it
// exercises every branch the unit test can't reach. Skips cleanly
// without a DB (Postgres+migrations) so `go test ./...` on a laptop
// stays green.
func TestVerifyOTP_Integration(t *testing.T) {
	pool := setupTestPool(t)
	svc := NewService(nil, pool) // Client is unused for VerifyOTP.
	_, schema, cleanup := makeTestProject(t, pool)
	defer cleanup()

	const phoneA = "+33612345678"
	const phoneB = "+33698765432"
	const phoneUnknown = "+33600000000"
	userA := insertPhoneUser(t, pool, schema, phoneA)
	userB := insertPhoneUser(t, pool, schema, phoneB)
	_ = userB

	ctx := context.Background()

	t.Run("correct code verifies immediately", func(t *testing.T) {
		code := seedOTP(t, pool, schema, userA)
		got, err := svc.VerifyOTP(ctx, schema, phoneA, code)
		if err != nil {
			t.Fatalf("correct code should verify, got err: %v", err)
		}
		if got != userA {
			t.Errorf("verify returned userID %q, want %q", got, userA)
		}
	})

	t.Run("cap kills the token at 5 wrong guesses", func(t *testing.T) {
		correctCode := seedOTP(t, pool, schema, userA)
		wrongCode := "000000"
		if wrongCode == correctCode { // guard against 1-in-10^6 flake
			wrongCode = "111111"
		}
		for i := 1; i <= 5; i++ {
			_, err := svc.VerifyOTP(ctx, schema, phoneA, wrongCode)
			if err == nil {
				t.Fatalf("wrong guess #%d should have errored, but succeeded", i)
			}
			if err.Error() != "verify phone otp: invalid or expired code" {
				t.Errorf("wrong guess #%d: unexpected error string: %v", i, err)
			}
		}
		// The 6th attempt with the CORRECT code must be rejected —
		// the token was killed by attempt #5.
		if _, err := svc.VerifyOTP(ctx, schema, phoneA, correctCode); err == nil {
			t.Fatal("correct code after cap should be rejected — token wasn't killed at attempts=5")
		}
	})

	t.Run("phone-binding: code for phone A cannot verify against phone B", func(t *testing.T) {
		codeForA := seedOTP(t, pool, schema, userA)
		// phone B has no active token; even with A's valid code, B
		// must not verify. This is the CORE #233 property — pre-fix,
		// `WHERE token_hash = $1` alone would have matched.
		if _, err := svc.VerifyOTP(ctx, schema, phoneB, codeForA); err == nil {
			t.Fatal("code intended for phone A must not verify phone B (pre-#233 regression)")
		}
	})

	t.Run("no enumeration: unknown / wrong / exhausted / expired all return same string", func(t *testing.T) {
		wantMsg := "verify phone otp: invalid or expired code"

		// (a) unknown phone
		_, err := svc.VerifyOTP(ctx, schema, phoneUnknown, "123456")
		if err == nil || err.Error() != wantMsg {
			t.Errorf("unknown phone: got %v, want %q", err, wantMsg)
		}

		// (b) wrong code for known phone with active token
		_ = seedOTP(t, pool, schema, userA)
		_, err = svc.VerifyOTP(ctx, schema, phoneA, "000000")
		if err == nil || err.Error() != wantMsg {
			t.Errorf("wrong code (known phone): got %v, want %q", err, wantMsg)
		}

		// (c) expired token: seed then set expires_at in the past
		_ = seedOTP(t, pool, schema, userA)
		_ = edb.RunAsAuthService(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				fmt.Sprintf(`UPDATE %q.email_tokens SET expires_at = now() - interval '1 second'
				             WHERE user_id = $1 AND token_type = 'phone_verification'`, schema),
				userA)
			return e
		})
		_, err = svc.VerifyOTP(ctx, schema, phoneA, "123456")
		if err == nil || err.Error() != wantMsg {
			t.Errorf("expired token: got %v, want %q", err, wantMsg)
		}

		// (d) exhausted token (attempts >= max, but not yet expired)
		codeExh := seedOTP(t, pool, schema, userA)
		_ = edb.RunAsAuthService(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				fmt.Sprintf(`UPDATE %q.email_tokens SET attempts = 5, used_at = now()
				             WHERE user_id = $1 AND token_type = 'phone_verification'
				               AND expires_at > now()`, schema),
				userA)
			return e
		})
		_, err = svc.VerifyOTP(ctx, schema, phoneA, codeExh)
		if err == nil || err.Error() != wantMsg {
			t.Errorf("exhausted token: got %v, want %q", err, wantMsg)
		}
	})
}
