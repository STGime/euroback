package audit

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setupAuditTest creates an isolated project to scope a hash chain to, and
// returns the audit service, that project id, the pool (for tamper
// injection), and a cleanup. Skips when no test database is reachable.
// Mirrors internal/vault/service_test.go's setup but skips tenant
// provisioning — audit_log lives in the public schema.
func setupAuditTest(t *testing.T) (*Service, string, *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}

	hankoUserID := fmt.Sprintf("test-audit-%d", os.Getpid())
	var ownerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (hanko_user_id, email)
		 VALUES ($1, $2)
		 ON CONFLICT (hanko_user_id) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		hankoUserID, "audittest@eurobase.app",
	).Scan(&ownerID); err != nil {
		pool.Close()
		t.Skipf("cannot create test platform user: %v", err)
	}

	slug := fmt.Sprintf("test-audit-%d", os.Getpid())
	var projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'provisioning')
		 RETURNING id`,
		ownerID, "Audit Test", slug, "tenant_test_audit", "eurobase-test-audit", "fr-par", "free",
	).Scan(&projectID); err != nil {
		pool.Close()
		t.Skipf("cannot create test project: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		// eurobase_api (the default test role) retains DELETE; the runtime
		// roles do not. Remove the chain rows before the project so they
		// don't fall back to NULL project and pollute the global chain.
		_, _ = pool.Exec(ctx, `DELETE FROM public.audit_log WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
		pool.Close()
	})

	return NewService(pool), projectID, pool
}

// TestAuditChain_VerifyCleanAndDetectTamper appends a few entries, confirms
// the chain verifies clean, then mutates a row directly in the DB and
// confirms Verify flags the break — the core tamper-evidence guarantee.
func TestAuditChain_VerifyCleanAndDetectTamper(t *testing.T) {
	svc, projectID, pool := setupAuditTest(t)
	ctx := context.Background()

	svc.Log(ctx, projectID, "", "admin@eurobase.app", "test.one")
	svc.Log(ctx, projectID, "", "admin@eurobase.app", "test.two",
		WithMetadata(map[string]interface{}{"k": "v"}))
	svc.Log(ctx, projectID, "", "admin@eurobase.app", "test.three", WithIP("203.0.113.7"))

	res, err := svc.Verify(ctx, projectID)
	if err != nil {
		t.Fatalf("Verify (clean): %v", err)
	}
	if !res.OK {
		t.Fatalf("clean chain reported broken at %s: %s", res.BrokenAtID, res.Reason)
	}
	if res.Checked != 3 {
		t.Errorf("Checked = %d, want 3", res.Checked)
	}

	// Tamper: alter a row's action in place. The test role retains UPDATE;
	// the runtime roles are revoked in migration 000058. If this environment
	// also revokes it for the test role, skip the detection assertion.
	tag, err := pool.Exec(ctx,
		`UPDATE public.audit_log SET action = 'tampered' WHERE project_id = $1 AND action = 'test.two'`,
		projectID)
	if err != nil {
		t.Skipf("cannot inject tamper (UPDATE denied for test role): %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expected to tamper exactly 1 row, affected %d", tag.RowsAffected())
	}

	res2, err := svc.Verify(ctx, projectID)
	if err != nil {
		t.Fatalf("Verify (tampered): %v", err)
	}
	if res2.OK {
		t.Error("Verify did not detect the tampered row")
	}
	if res2.BrokenAtID == "" {
		t.Error("Verify reported a break but no offending row id")
	}
}

// TestAuditChain_DetectDeletion confirms that removing a row mid-chain breaks
// the linkage check (the next row's prev_hash no longer matches).
func TestAuditChain_DetectDeletion(t *testing.T) {
	svc, projectID, pool := setupAuditTest(t)
	ctx := context.Background()

	svc.Log(ctx, projectID, "", "admin@eurobase.app", "del.one")
	svc.Log(ctx, projectID, "", "admin@eurobase.app", "del.two")
	svc.Log(ctx, projectID, "", "admin@eurobase.app", "del.three")

	tag, err := pool.Exec(ctx,
		`DELETE FROM public.audit_log WHERE project_id = $1 AND action = 'del.two'`, projectID)
	if err != nil {
		t.Skipf("cannot inject deletion (DELETE denied for test role): %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expected to delete exactly 1 row, deleted %d", tag.RowsAffected())
	}

	res, err := svc.Verify(ctx, projectID)
	if err != nil {
		t.Fatalf("Verify (after deletion): %v", err)
	}
	if res.OK {
		t.Error("Verify did not detect the mid-chain deletion")
	}
}
