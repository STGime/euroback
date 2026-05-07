package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Closes advisory GHSA-7428-mvpp-rhr7 (C3) layer 1.
//
// These tests exercise the per-tenant Postgres role architecture
// installed by migration 000047. The function runner connects as
// `eurobase_function_runner` and runs each invocation inside a
// transaction with `SET LOCAL ROLE <schema>_func` + `SET LOCAL
// search_path TO <schema>`. The per-tenant role has grants only on its
// own schema, so cross-tenant SQL fails at the role-permission layer
// even if the runner's process is compromised and search_path is
// bypassed.
//
// All tests skip cleanly if the runner role isn't bootstrapped on the
// local test DB. To run locally: ensure `eurobase_function_runner` and
// the per-tenant `<schema>_func` roles exist (they're created by
// 000047 against an existing tenant). The migrate Job creates them in
// real environments.
//
// Three threats covered here:
//   1. Cross-tenant SELECT must be denied.
//   2. Cross-tenant INSERT must be denied.
//   3. Public-schema platform tables (projects, api_keys) must be
//      unreachable even with the runner connection's full session.

const runnerRoleName = "eurobase_function_runner"

// runnerPool opens a pgxpool as the function-runner login role. Skips
// the test cleanly if the role isn't present locally.
func runnerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	host := os.Getenv("TEST_PGHOST")
	if host == "" {
		host = "localhost:5433"
	}
	// The local password convention matches the other roles in
	// devrole_test.go.
	connStr := "postgres://" + runnerRoleName + ":localdev@" + host + "/eurobase?sslmode=disable"
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Skipf("cannot connect as %s (role bootstrap not run locally): %v", runnerRoleName, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping as %s: %v", runnerRoleName, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// requirePerTenantRole checks that the per-tenant function role for
// schemaName exists. Skips otherwise (migration 000047 hasn't been
// applied to the local DB).
func requirePerTenantRole(t *testing.T, adminPool *pgxpool.Pool, schemaName string) {
	t.Helper()
	roleName := schemaName + "_func"
	var exists bool
	err := adminPool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, roleName,
	).Scan(&exists)
	if err != nil {
		t.Skipf("cannot check role existence: %v", err)
	}
	if !exists {
		t.Skipf("per-tenant role %s does not exist — apply migration 000047", roleName)
	}
}

// runWithRole opens a tx, applies SET LOCAL ROLE + search_path, runs fn.
// Mirrors the runner's per-invocation pattern.
func runWithRole(ctx context.Context, pool *pgxpool.Pool, schemaName string, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	roleName := schemaName + "_func"
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL ROLE "%s"`, roleName)); err != nil {
		return fmt.Errorf("set local role: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL search_path TO "%s"`, schemaName)); err != nil {
		return fmt.Errorf("set local search_path: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TestFunctionRunnerRole_OwnTenantSelectAllowed verifies that SELECTs
// against the runner's *own* tenant schema succeed via SET LOCAL ROLE.
func TestFunctionRunnerRole_OwnTenantSelectAllowed(t *testing.T) {
	adminPool, schemaA, _ := setupTestDB(t)
	requirePerTenantRole(t, adminPool, schemaA)

	rPool := runnerPool(t)
	ctx := context.Background()

	err := runWithRole(ctx, rPool, schemaA, func(tx pgx.Tx) error {
		var n int
		// `users` is a tenant-schema table. SELECT count(*) succeeds if
		// the role has USAGE on the schema and SELECT on the table.
		return tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	})
	if err != nil {
		t.Errorf("own-tenant SELECT failed: %v", err)
	}
}

// TestFunctionRunnerRole_CrossTenantSelectDenied verifies that under
// SET LOCAL ROLE for tenant A, a SELECT against tenant B fails with
// permission denied — independent of search_path.
func TestFunctionRunnerRole_CrossTenantSelectDenied(t *testing.T) {
	adminPoolA, schemaA, _ := setupTestDB(t)
	adminPoolB, schemaB, _ := setupTestDB(t)
	_ = adminPoolB // second tenant exists in the same DB
	requirePerTenantRole(t, adminPoolA, schemaA)
	requirePerTenantRole(t, adminPoolA, schemaB)

	if schemaA == schemaB {
		t.Fatalf("setupTestDB produced same schema for both tenants: %s", schemaA)
	}

	rPool := runnerPool(t)
	ctx := context.Background()

	// As tenant A's role, try to read tenant B's users table.
	err := runWithRole(ctx, rPool, schemaA, func(tx pgx.Tx) error {
		var n int
		return tx.QueryRow(ctx,
			fmt.Sprintf(`SELECT count(*) FROM "%s".users`, schemaB),
		).Scan(&n)
	})
	if err == nil {
		t.Fatalf("cross-tenant SELECT succeeded — role isolation is broken")
	}
	if !isPermissionDenied(err) {
		t.Errorf("cross-tenant SELECT failed but with unexpected error: %v", err)
	}
}

// TestFunctionRunnerRole_CrossTenantInsertDenied verifies that under
// SET LOCAL ROLE for tenant A, an INSERT into tenant B fails. Even if
// somebody bypassed the SELECT block they shouldn't be able to mutate
// other tenants.
func TestFunctionRunnerRole_CrossTenantInsertDenied(t *testing.T) {
	adminPoolA, schemaA, _ := setupTestDB(t)
	adminPoolB, schemaB, _ := setupTestDB(t)
	_ = adminPoolB
	requirePerTenantRole(t, adminPoolA, schemaA)
	requirePerTenantRole(t, adminPoolA, schemaB)

	rPool := runnerPool(t)
	ctx := context.Background()

	err := runWithRole(ctx, rPool, schemaA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			fmt.Sprintf(`INSERT INTO "%s".users (email, display_name) VALUES ($1, $2)`, schemaB),
			"crossattack@example.com", "crossattack",
		)
		return err
	})
	if err == nil {
		t.Fatalf("cross-tenant INSERT succeeded — role isolation is broken")
	}
	if !isPermissionDenied(err) {
		t.Errorf("cross-tenant INSERT failed but with unexpected error: %v", err)
	}
}

// TestFunctionRunnerRole_PublicTablesUnreachable verifies that even
// without SET LOCAL ROLE — i.e. with the bare runner-role session — the
// connection cannot read platform tables in `public.*`. The runner role
// has USAGE on `public` (so it can call helper functions) but no SELECT
// on the platform tables.
func TestFunctionRunnerRole_PublicTablesUnreachable(t *testing.T) {
	rPool := runnerPool(t)
	ctx := context.Background()

	for _, table := range []string{"projects", "api_keys", "platform_users"} {
		var n int
		err := rPool.QueryRow(ctx,
			fmt.Sprintf(`SELECT count(*) FROM public.%s`, table),
		).Scan(&n)
		if err == nil {
			t.Errorf("runner role has SELECT on public.%s — that's a leak", table)
			continue
		}
		if !isPermissionDenied(err) {
			t.Errorf("public.%s SELECT failed but with unexpected error: %v", table, err)
		}
	}
}

// TestFunctionRunnerRole_RoleResetsOnCommit verifies that SET LOCAL
// ROLE is tx-scoped — a subsequent statement on the same connection
// without an explicit SET LOCAL ROLE runs as plain runner role (which
// has no schema grants), not as the previous tenant's role. This is
// load-bearing: the runner's connection pool reuses connections, and
// role leakage between invocations would be a cross-tenant leak.
func TestFunctionRunnerRole_RoleResetsOnCommit(t *testing.T) {
	adminPool, schemaA, _ := setupTestDB(t)
	requirePerTenantRole(t, adminPool, schemaA)

	rPool := runnerPool(t)
	ctx := context.Background()

	// Acquire one connection so we can verify state on the same
	// physical connection across two transactions.
	conn, err := rPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	// First tx: become tenant A, SELECT, commit.
	tx1, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	if _, err := tx1.Exec(ctx, fmt.Sprintf(`SET LOCAL ROLE "%s_func"`, schemaA)); err != nil {
		t.Fatalf("set local role: %v", err)
	}
	if _, err := tx1.Exec(ctx, fmt.Sprintf(`SET LOCAL search_path TO "%s"`, schemaA)); err != nil {
		t.Fatalf("set local search_path: %v", err)
	}
	var n int
	if err := tx1.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("tx1 select: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}

	// Second tx on the SAME connection: do NOT set role. Try to SELECT
	// from tenant A. Should fail because the bare runner role has no
	// USAGE on the tenant schema.
	tx2, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer tx2.Rollback(ctx) //nolint:errcheck
	err = tx2.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM "%s".users`, schemaA),
	).Scan(&n)
	if err == nil {
		t.Errorf("SELECT succeeded without SET LOCAL ROLE — role leaked across transactions on the same connection")
	} else if !isPermissionDenied(err) {
		t.Errorf("expected permission denied on bare runner connection, got: %v", err)
	}
}

// TestFunctionRunnerRole_RunnerCannotSetArbitraryRole verifies that the
// runner can only `SET ROLE` to roles it's a member of (the per-tenant
// `<schema>_func` roles). Trying to escalate to migrator or gateway
// must fail.
func TestFunctionRunnerRole_RunnerCannotSetArbitraryRole(t *testing.T) {
	rPool := runnerPool(t)
	ctx := context.Background()

	for _, target := range []string{"eurobase_migrator", "eurobase_gateway", "eurobase_developer", "eurobase_api"} {
		_, err := rPool.Exec(ctx, fmt.Sprintf(`SET ROLE "%s"`, target))
		if err == nil {
			// Reset just in case.
			_, _ = rPool.Exec(ctx, "RESET ROLE")
			t.Errorf("runner role escalated to %s — that's a privilege break", target)
			continue
		}
		// "permission denied to set role" or similar.
		if !strings.Contains(strings.ToLower(err.Error()), "permission") &&
			!strings.Contains(strings.ToLower(err.Error()), "must be member") {
			t.Logf("escalation to %s correctly denied with: %v", target, err)
		}
	}
}

// isPermissionDenied returns true if err is a Postgres permission-denied
// error (SQLSTATE 42501).
func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42501"
	}
	// pgx wraps PgError differently — fall back to substring check on
	// the error message.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission denied") || strings.Contains(msg, "sqlstate 42501")
}

// pgError is a minimal interface to unwrap pgx PgError without forcing
// a direct dependency on the internal struct shape.
type pgError struct {
	Code string
}

func (e *pgError) Error() string { return "pg error: " + e.Code }
