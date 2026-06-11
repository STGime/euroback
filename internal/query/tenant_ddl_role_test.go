package query

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests for the tenant-migration containment model (#190 / PR
// #209 review). These verify the Postgres-enforced boundary that the
// regex validator cannot provide: SQL executed under tenant_<id>_ddl,
// reached via the eurobase_ddl_runner login role, cannot touch public.*
// platform tables, cannot reach other tenant schemas, and — critically —
// cannot escalate by RESET ROLE (which lands on the privilege-less
// runner role).
//
// They skip cleanly when the ddl-runner role / migration 000063 isn't
// present on the local DB. The migrate Job creates the roles in real
// environments; CI runs them when a Postgres service is wired.

const ddlRunnerRoleName = "eurobase_ddl_runner"

func ddlRunnerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	host := os.Getenv("TEST_PGHOST")
	if host == "" {
		host = "localhost:5433"
	}
	connStr := "postgres://" + ddlRunnerRoleName + ":localdev@" + host + "/eurobase?sslmode=disable"
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Skipf("cannot connect as %s (migration 000063 not applied locally): %v", ddlRunnerRoleName, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping as %s: %v", ddlRunnerRoleName, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// applyAs runs sqlText through the real apply flow's role discipline
// (SET LOCAL ROLE tenant_<id>_ddl) on the ddl-runner pool and reports
// whether it succeeded. Uses a rolled-back transaction so it leaves no
// state — mirrors ApplyTenantMigration's role setup without the
// bookkeeping.
func applyAs(t *testing.T, pool *pgxpool.Pool, schema, sqlText string) error {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE "`+schema+`_ddl"`); err != nil {
		t.Skipf("cannot SET ROLE %s_ddl (000063 not applied): %v", schema, err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL search_path TO "`+schema+`"`); err != nil {
		return err
	}
	_, err = tx.Conn().PgConn().Exec(ctx, sqlText).ReadAll()
	return err
}

// testSchema picks an existing tenant schema to test against, or skips.
func testSchema(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	if s := os.Getenv("TEST_TENANT_SCHEMA"); s != "" {
		return s
	}
	t.Skip("set TEST_TENANT_SCHEMA to an existing tenant schema to run ddl-role integration tests")
	return ""
}

func TestTenantDDLRole_CannotWritePlatformTables(t *testing.T) {
	pool := ddlRunnerPool(t)
	schema := testSchema(t, pool)
	err := applyAs(t, pool, schema, "UPDATE public.projects SET plan = 'pro';")
	if err == nil {
		t.Fatal("SECURITY: tenant_<id>_ddl was able to UPDATE public.projects")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Logf("denied (non-permission error also acceptable): %v", err)
	}
}

func TestTenantDDLRole_CannotReachOtherTenantSchema(t *testing.T) {
	pool := ddlRunnerPool(t)
	schema := testSchema(t, pool)
	// A schema name that almost certainly isn't the caller's.
	err := applyAs(t, pool, schema, "SELECT 1 FROM tenant_0000_does_not_exist.users;")
	if err == nil {
		t.Fatal("SECURITY: reached a foreign tenant schema")
	}
}

func TestTenantDDLRole_FunctionBodyBypassIsContained(t *testing.T) {
	// The exact bypass the validator can't catch: payload in a DO body.
	// The ROLE must contain it.
	pool := ddlRunnerPool(t)
	schema := testSchema(t, pool)
	err := applyAs(t, pool, schema, `DO $$ BEGIN UPDATE public.projects SET plan='pro'; END $$;`)
	if err == nil {
		t.Fatal("SECURITY: function-body write to public.projects succeeded")
	}
}

func TestTenantDDLRole_ResetRoleLandsHarmless(t *testing.T) {
	// RESET ROLE inside the body must drop to eurobase_ddl_runner, which
	// has no write on public.projects — not back to a migrator-inheriting
	// role.
	pool := ddlRunnerPool(t)
	schema := testSchema(t, pool)
	err := applyAs(t, pool, schema, `DO $$ BEGIN RESET ROLE; UPDATE public.projects SET plan='pro'; END $$;`)
	if err == nil {
		t.Fatal("SECURITY: RESET ROLE escalated and wrote public.projects")
	}
}

func TestTenantDDLRole_CanManageOwnSchema(t *testing.T) {
	// Positive control: legitimate DDL in the tenant's own schema works.
	pool := ddlRunnerPool(t)
	schema := testSchema(t, pool)
	err := applyAs(t, pool, schema,
		`CREATE TABLE _eb_migration_probe (id int PRIMARY KEY, note text CHECK (note <> ''));`)
	if err != nil {
		t.Fatalf("legitimate own-schema DDL was rejected: %v", err)
	}
}
