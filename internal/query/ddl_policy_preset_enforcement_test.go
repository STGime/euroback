package query

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// End-to-end RLS enforcement tests for the presets emitted by
// ApplyPolicyPreset. Where ddl_policy_preset_test.go verifies the
// *shape* of the emitted policy DDL (rows in pg_policies), these
// tests verify that Postgres actually enforces those policies the
// way the customer would experience them — via a non-owner role
// with the app.end_user_role GUC set to what the SDK/REST runtime
// would set for a secret-key vs anon-key call.
//
// The runtime setup Postgres is asked to reproduce:
//
//   - Owner of tenant tables: the migrator role (via setupTestDB
//     -> provision_tenant), which is what the customer's project
//     has in production too.
//   - Runtime role that actually executes the SDK/REST INSERT/SELECT:
//     a non-owner role with USAGE on the schema + DML on the
//     specific table. In prod this is eurobase_gateway. Here we
//     create a per-test throwaway role so the test is portable
//     (doesn't depend on eurobase_gateway existing in the local
//     dev DB — setup-local.sh doesn't create it).
//   - The app.end_user_role GUC is set inside the tx BEFORE the
//     INSERT/SELECT, matching internal/query/engine.go
//     applyRLSContext lines 74-95. "service" = secret key,
//     "anon" = public key without a signed-in end-user.
//
// If enforcement is missing on the runtime role (RLS not applying
// because the role turns out to bypass it) the tests would produce
// false positives, so each test also asserts the negative case
// (anon key denied) to prove enforcement is really happening.

// mkRuntimeRole creates a throwaway PG login role, grants it
// USAGE on the given tenant schema + full DML on the given table,
// and returns a cleanup func. The role name is randomised so
// parallel test runs don't collide.
func mkRuntimeRole(t *testing.T, pool *pgxpool.Pool, schema, table string) (string, func()) {
	t.Helper()
	ctx := context.Background()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random: %v", err)
	}
	roleName := "test_rls_runtime_" + hex.EncodeToString(b[:])
	// pw doesn't matter — we authenticate via SET LOCAL ROLE, not
	// via a new connection. The role just needs LOGIN so it's a
	// realistic runtime shape (matches eurobase_gateway).
	pw := "irrelevant"
	sqls := []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", roleName, pw),
		// PG16: a CREATEROLE non-superuser CAN create a role but
		// CANNOT SET ROLE to it without an explicit membership +
		// SET option. Grant the connecting session role membership
		// with the SET privilege so `SET LOCAL ROLE <throwaway>`
		// below works regardless of whether the test DB connects
		// as a superuser or as eurobase_api. Without this the tests
		// silently only run on superuser connections and give a
		// false sense of coverage everywhere else.
		fmt.Sprintf("GRANT %s TO CURRENT_USER WITH SET TRUE", roleName),
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", quoteIdent(schema), roleName),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO %s", qualifiedTable(schema, table), roleName),
		// The is_service_role() lookup lives in public — the role
		// needs USAGE + EXECUTE to evaluate it inside the RLS policy.
		"GRANT USAGE ON SCHEMA public TO " + roleName,
		fmt.Sprintf("GRANT EXECUTE ON FUNCTION public.is_service_role() TO %s", roleName),
	}
	for _, s := range sqls {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("setup runtime role SQL %q: %v", s, err)
		}
	}
	cleanup := func() {
		ctx := context.Background()
		// Revoke first so DROP ROLE isn't blocked by dependent
		// privileges. Best-effort — the test tenant schema is dropped
		// on t.Cleanup anyway.
		_, _ = pool.Exec(ctx, fmt.Sprintf("REVOKE ALL ON %s FROM %s", qualifiedTable(schema, table), roleName))
		_, _ = pool.Exec(ctx, fmt.Sprintf("REVOKE ALL ON SCHEMA %s FROM %s", quoteIdent(schema), roleName))
		_, _ = pool.Exec(ctx, "REVOKE ALL ON SCHEMA public FROM "+roleName)
		_, _ = pool.Exec(ctx, fmt.Sprintf("REVOKE EXECUTE ON FUNCTION public.is_service_role() FROM %s", roleName))
		_, _ = pool.Exec(ctx, "DROP ROLE IF EXISTS "+roleName)
	}
	return roleName, cleanup
}

// runAsRuntimeRole opens a tx, switches to the runtime role via
// SET LOCAL ROLE (matching how the gateway pool connects — the
// tx is bound to the role for its lifetime), sets the app.end_user_role
// GUC as the runtime would, and runs the caller-supplied SQL. Returns
// any error verbatim so callers can check for RLS-denied semantics.
func runAsRuntimeRole(t *testing.T, pool *pgxpool.Pool, schema, role, endUserRole, sql string, args ...any) (rows []map[string]any, err error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL ROLE %s", role)); err != nil {
		return nil, fmt.Errorf("set role: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", quoteIdent(schema))); err != nil {
		return nil, fmt.Errorf("set search_path: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.end_user_role', $1, true)", endUserRole); err != nil {
		return nil, fmt.Errorf("set end_user_role: %w", err)
	}
	pgRows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	scanned, scanErr := scanRows(pgRows)
	if scanErr != nil {
		return nil, scanErr
	}
	// Commit so INSERTs land — otherwise the deferred rollback
	// undoes the change and follow-up SELECTs would find nothing.
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return scanned, nil
}

// TestPolicyEnforcement_ServiceOnly — the customer's requested
// pattern. Under service_only, the runtime role acting AS ANON
// must be denied every op; acting AS SERVICE must be allowed.
// This is what makes the browser SDK "physically unable to reach
// the row" contract we sold to the customer.
func TestPolicyEnforcement_ServiceOnly(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	tbl := "svc_only_enf"
	mkPresetTable(t, pool, schema, tbl)
	if err := ApplyPolicyPreset(context.Background(), pool, schema, tbl, "service_only", "user_id"); err != nil {
		t.Fatalf("ApplyPolicyPreset(service_only): %v", err)
	}
	role, cleanup := mkRuntimeRole(t, pool, schema, tbl)
	defer cleanup()

	// SERVICE: INSERT allowed, SELECT returns the row.
	if _, err := runAsRuntimeRole(t, pool, schema, role, "service",
		fmt.Sprintf("INSERT INTO %s (data) VALUES ('svc-row') RETURNING id", qualifiedTable(schema, tbl))); err != nil {
		t.Fatalf("service-role INSERT should succeed under service_only, got: %v", err)
	}
	svcRows, err := runAsRuntimeRole(t, pool, schema, role, "service",
		fmt.Sprintf("SELECT id, data FROM %s", qualifiedTable(schema, tbl)))
	if err != nil {
		t.Fatalf("service-role SELECT: %v", err)
	}
	if len(svcRows) != 1 {
		t.Fatalf("service-role SELECT under service_only should see the inserted row (want 1, got %d)", len(svcRows))
	}

	// ANON: INSERT denied with the exact Postgres error class our
	// customer would see; SELECT returns zero rows (RLS-filtered).
	_, insErr := runAsRuntimeRole(t, pool, schema, role, "anon",
		fmt.Sprintf("INSERT INTO %s (data) VALUES ('anon-row') RETURNING id", qualifiedTable(schema, tbl)))
	if insErr == nil {
		t.Fatalf("anon-role INSERT MUST be denied under service_only — got nil error (RLS not enforced?)")
	}
	if !strings.Contains(insErr.Error(), "row-level security") && !strings.Contains(insErr.Error(), "42501") {
		t.Errorf("anon-role INSERT error should be RLS-denial; got: %v", insErr)
	}
	anonRows, err := runAsRuntimeRole(t, pool, schema, role, "anon",
		fmt.Sprintf("SELECT id, data FROM %s", qualifiedTable(schema, tbl)))
	if err != nil {
		t.Fatalf("anon-role SELECT: %v", err)
	}
	if len(anonRows) != 0 {
		t.Errorf("anon-role SELECT under service_only should see zero rows; got %d: %+v", len(anonRows), anonRows)
	}
}

// TestPolicyEnforcement_ReadOnly_ServiceCanWrite — regression
// coverage for the customer's exact complaint. Before the fix, the
// service role could not INSERT under read_only (only the SELECT
// policy existed; Postgres denied everything else by default). This
// test confirms:
//   - service can INSERT AND SELECT (fix works)
//   - anon can SELECT (public reads preserved — the whole point of
//     read_only) but CANNOT INSERT (still restricted)
func TestPolicyEnforcement_ReadOnly_ServiceCanWrite(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	tbl := "read_only_enf"
	mkPresetTable(t, pool, schema, tbl)
	if err := ApplyPolicyPreset(context.Background(), pool, schema, tbl, "read_only", "user_id"); err != nil {
		t.Fatalf("ApplyPolicyPreset(read_only): %v", err)
	}
	role, cleanup := mkRuntimeRole(t, pool, schema, tbl)
	defer cleanup()

	// SERVICE: INSERT allowed under the new companion policy.
	if _, err := runAsRuntimeRole(t, pool, schema, role, "service",
		fmt.Sprintf("INSERT INTO %s (data) VALUES ('svc-write') RETURNING id", qualifiedTable(schema, tbl))); err != nil {
		t.Fatalf("service-role INSERT should succeed under read_only (was broken pre-fix); got: %v", err)
	}
	// SERVICE SELECT works (both policies permit).
	svcRows, err := runAsRuntimeRole(t, pool, schema, role, "service",
		fmt.Sprintf("SELECT data FROM %s", qualifiedTable(schema, tbl)))
	if err != nil {
		t.Fatalf("service-role SELECT: %v", err)
	}
	if len(svcRows) != 1 {
		t.Fatalf("service-role SELECT under read_only should see 1 row; got %d", len(svcRows))
	}

	// ANON: SELECT allowed (the "read_only" contract for public
	// callers). Row inserted by the service call above is visible.
	anonRows, err := runAsRuntimeRole(t, pool, schema, role, "anon",
		fmt.Sprintf("SELECT data FROM %s", qualifiedTable(schema, tbl)))
	if err != nil {
		t.Fatalf("anon-role SELECT: %v", err)
	}
	if len(anonRows) != 1 {
		t.Errorf("anon-role SELECT under read_only should see the row (want 1, got %d)", len(anonRows))
	}

	// ANON INSERT/UPDATE/DELETE must still be denied.
	_, insErr := runAsRuntimeRole(t, pool, schema, role, "anon",
		fmt.Sprintf("INSERT INTO %s (data) VALUES ('anon-write') RETURNING id", qualifiedTable(schema, tbl)))
	if insErr == nil {
		t.Fatalf("anon-role INSERT MUST be denied under read_only — got nil error")
	}
	if !strings.Contains(insErr.Error(), "row-level security") && !strings.Contains(insErr.Error(), "42501") {
		t.Errorf("anon-role INSERT error should be RLS-denial; got: %v", insErr)
	}
}

// TestPolicyEnforcement_OwnerAccess_ServiceBypass — existing
// preset, but locking in the load-bearing contract: the service
// role bypasses the owner check on every op. If a future refactor
// dropped the `OR public.is_service_role()` branch, every backend
// consumer who owns rows on behalf of end-users would silently
// break.
func TestPolicyEnforcement_OwnerAccess_ServiceBypass(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	tbl := "owner_access_enf"
	mkPresetTable(t, pool, schema, tbl)
	if err := ApplyPolicyPreset(context.Background(), pool, schema, tbl, "owner_access", "user_id"); err != nil {
		t.Fatalf("ApplyPolicyPreset(owner_access): %v", err)
	}
	role, cleanup := mkRuntimeRole(t, pool, schema, tbl)
	defer cleanup()

	// SERVICE inserts a row on behalf of an arbitrary end-user id.
	// If the service bypass is intact, this succeeds. If a future
	// refactor drops the bypass, this fails because auth_uid() has
	// no end-user JWT to resolve to and the user_id check flunks.
	insRows, err := runAsRuntimeRole(t, pool, schema, role, "service",
		fmt.Sprintf("INSERT INTO %s (user_id, data) VALUES ('11111111-1111-1111-1111-111111111111', 'svc-behalf') RETURNING id", qualifiedTable(schema, tbl)))
	if err != nil {
		t.Fatalf("service-role INSERT on behalf of end-user MUST succeed under owner_access; got: %v", err)
	}
	if len(insRows) != 1 {
		t.Errorf("service INSERT should RETURN the row; got %d", len(insRows))
	}

	// SERVICE SELECT sees all rows.
	svcRows, err := runAsRuntimeRole(t, pool, schema, role, "service",
		fmt.Sprintf("SELECT id, user_id FROM %s", qualifiedTable(schema, tbl)))
	if err != nil {
		t.Fatalf("service-role SELECT: %v", err)
	}
	if len(svcRows) != 1 {
		t.Errorf("service SELECT should see all rows regardless of owner; got %d", len(svcRows))
	}

	// ANON without an end-user context sees zero rows (no owner
	// matches, no service bypass).
	anonRows, err := runAsRuntimeRole(t, pool, schema, role, "anon",
		fmt.Sprintf("SELECT id FROM %s", qualifiedTable(schema, tbl)))
	if err != nil {
		t.Fatalf("anon-role SELECT: %v", err)
	}
	if len(anonRows) != 0 {
		t.Errorf("anon-role SELECT under owner_access without matching user_id should see zero rows; got %d", len(anonRows))
	}
}

// TestPolicyEnforcement_FullAccess_NoDistinction — regression:
// full_access is intentionally the "no isolation" preset — anon
// and service both have full DML. This test locks that behaviour in
// so a future refactor doesn't accidentally add a service-check to
// this preset (which would be a breaking change for anyone using
// it for genuinely public data).
func TestPolicyEnforcement_FullAccess_NoDistinction(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	tbl := "full_access_enf"
	mkPresetTable(t, pool, schema, tbl)
	if err := ApplyPolicyPreset(context.Background(), pool, schema, tbl, "full_access", "user_id"); err != nil {
		t.Fatalf("ApplyPolicyPreset(full_access): %v", err)
	}
	role, cleanup := mkRuntimeRole(t, pool, schema, tbl)
	defer cleanup()

	// Both roles can INSERT — that's what "full_access" means.
	for _, endUserRole := range []string{"service", "anon"} {
		if _, err := runAsRuntimeRole(t, pool, schema, role, endUserRole,
			fmt.Sprintf("INSERT INTO %s (data) VALUES ('%s-row') RETURNING id", qualifiedTable(schema, tbl), endUserRole)); err != nil {
			t.Errorf("%s INSERT under full_access should succeed; got: %v", endUserRole, err)
		}
	}
}

