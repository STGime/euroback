package query

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─────────────────────────────────────────────────────────────────────
// Track A — privilege regression on the migration itself.
//
// These tests verify that the role topology is what 000043 says it is.
// They open ad-hoc connections as eurobase_developer and eurobase_gateway
// and skip cleanly if those roles aren't bootstrapped on the local DB
// (which is the common case until the developer adds them with the
// expected localdev password). Once a CI Postgres-backed test job
// exists, these run there too.
// ─────────────────────────────────────────────────────────────────────

func roleConn(t *testing.T, role, password string) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	host := os.Getenv("TEST_PGHOST")
	if host == "" {
		host = "localhost:5433"
	}
	connStr := "postgres://" + role + ":" + password + "@" + host + "/eurobase?sslmode=disable"
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Skipf("cannot connect as %s (role bootstrap not run locally): %v", role, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping as %s: %v", role, err)
	}
	return pool
}

func TestDeveloperRole_Attributes(t *testing.T) {
	pool, _, _ := setupTestDB(t)
	ctx := context.Background()

	var canLogin, inherit, isSuper, createRole, createDB bool
	err := pool.QueryRow(ctx,
		`SELECT rolcanlogin, rolinherit, rolsuper, rolcreaterole, rolcreatedb
		 FROM pg_roles WHERE rolname = 'eurobase_developer'`,
	).Scan(&canLogin, &inherit, &isSuper, &createRole, &createDB)
	if err != nil {
		t.Skipf("eurobase_developer role not present (000043 not applied): %v", err)
	}
	if !canLogin {
		t.Errorf("eurobase_developer must have LOGIN")
	}
	if !inherit {
		t.Errorf("eurobase_developer must have INHERIT (otherwise migrator privileges don't apply)")
	}
	if isSuper {
		t.Errorf("eurobase_developer must not be SUPERUSER")
	}
	if createRole {
		t.Errorf("eurobase_developer must not have CREATEROLE")
	}
	if createDB {
		t.Errorf("eurobase_developer must not have CREATEDB")
	}
}

func TestDeveloperRole_MemberOfMigrator(t *testing.T) {
	pool, _, _ := setupTestDB(t)
	ctx := context.Background()

	var member bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM pg_auth_members am
			JOIN pg_roles parent ON parent.oid = am.roleid
			JOIN pg_roles child ON child.oid = am.member
			WHERE parent.rolname = 'eurobase_migrator' AND child.rolname = 'eurobase_developer'
		 )`,
	).Scan(&member)
	if err != nil {
		t.Skipf("cannot query pg_auth_members: %v", err)
	}
	if !member {
		t.Error("eurobase_developer must be a member of eurobase_migrator (otherwise SET ROLE eurobase_migrator inside platform tx fails)")
	}
}

func TestDeveloperRole_DDLPrivileges(t *testing.T) {
	// Walks the exact failure shape from the original tester report:
	// CREATE TABLE referencing a migrator-owned table, ALTER TABLE
	// adding a column to a migrator-owned table. Must succeed when run
	// from a connection that's a member of eurobase_migrator with INHERIT.
	_, schema, _ := setupTestDB(t)

	dev := roleConn(t, "eurobase_developer", os.Getenv("TEST_DEV_PASSWORD"))
	defer dev.Close()
	ctx := context.Background()

	tx, err := dev.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE eurobase_migrator"); err != nil {
		t.Fatalf("SET LOCAL ROLE eurobase_migrator: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+pgIdent(schema)+", public"); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	// users is migrator-owned via provision_tenant.
	if _, err := tx.Exec(ctx, "CREATE TABLE foo (id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(), user_id uuid REFERENCES users(id) ON DELETE CASCADE)"); err != nil {
		t.Fatalf("CREATE TABLE foo with REFERENCES users: %v", err)
	}
	if _, err := tx.Exec(ctx, "ALTER TABLE users ADD COLUMN scratch_dev_role_test text"); err != nil {
		t.Fatalf("ALTER TABLE users: %v", err)
	}
	// Tx is rolled back via defer; no schema drift.
}

func TestGatewayRole_DDLPermissionDenied(t *testing.T) {
	// Negative half: the same operations must fail as eurobase_gateway,
	// which is NOT a member of migrator. This is what would have caught
	// the original silent-truncation bug.
	_, schema, _ := setupTestDB(t)

	gw := roleConn(t, "eurobase_gateway", os.Getenv("TEST_GATEWAY_PASSWORD"))
	defer gw.Close()
	ctx := context.Background()

	tx, err := gw.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+pgIdent(schema)+", public"); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	_, err = tx.Exec(ctx, "ALTER TABLE users ADD COLUMN scratch_gw_role_test text")
	if err == nil {
		t.Fatal("ALTER TABLE users as eurobase_gateway must fail; the role split is broken")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "permission denied") && !strings.Contains(strings.ToLower(err.Error()), "must be owner") {
		t.Errorf("expected 'permission denied' or 'must be owner', got: %v", err)
	}

	// SET LOCAL ROLE eurobase_migrator must also fail for gateway —
	// the whole point of the role separation.
	_, err = tx.Exec(ctx, "SET LOCAL ROLE eurobase_migrator")
	if err == nil {
		t.Error("eurobase_gateway must NOT be able to SET ROLE eurobase_migrator (would defeat the role split)")
	}
}

// ─────────────────────────────────────────────────────────────────────
// Track B — engine context-flag wiring.
//
// Tests that the QueryEngine reads DeveloperRoleFromContext and runs
// SET LOCAL ROLE eurobase_migrator at the right point. We exercise this
// against the existing test pool (eurobase_api) which is itself a
// member of eurobase_migrator — so the role switch works on the same
// connection without bootstrapping additional credentials.
// ─────────────────────────────────────────────────────────────────────

func TestExecuteSQL_DeveloperRole_RunsAsMigrator(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	engine := NewQueryEngine(pool)

	// With flag set: current_user inside the tx should be eurobase_migrator.
	ctx := WithDeveloperRole(ContextWithSchema(context.Background(), schema))
	cols, rows, err := engine.ExecuteSQL(ctx, schema, "SELECT current_user", 1)
	if err != nil {
		t.Fatalf("ExecuteSQL with developer role: %v", err)
	}
	if len(rows) != 1 || len(cols) == 0 {
		t.Fatalf("expected 1 row, got %d (cols=%v)", len(rows), cols)
	}
	got, _ := rows[0]["current_user"].(string)
	if got != "eurobase_migrator" {
		t.Errorf("with developer-role flag, current_user = %q, want %q", got, "eurobase_migrator")
	}

	// Without flag: should NOT be eurobase_migrator.
	ctx2 := ContextWithSchema(context.Background(), schema)
	_, rows2, err := engine.ExecuteSQL(ctx2, schema, "SELECT current_user", 1)
	if err != nil {
		t.Fatalf("ExecuteSQL without developer role: %v", err)
	}
	got2, _ := rows2[0]["current_user"].(string)
	if got2 == "eurobase_migrator" {
		t.Error("without developer-role flag, current_user must NOT be eurobase_migrator (the elevation should be opt-in)")
	}
}

func TestExecuteSQLTransaction_DeveloperRole_AppliesMigratorOwnership(t *testing.T) {
	// Migration shape from the actual failing tester case: create a
	// table, add a column to a migrator-owned table, create a child
	// table with FK back into the new one. Must complete atomically
	// when the developer-role flag is set.
	pool, schema, _ := setupTestDB(t)
	engine := NewQueryEngine(pool)

	ctx := WithDeveloperRole(ContextWithSchema(context.Background(), schema))
	results, err := engine.ExecuteSQLTransaction(ctx, schema, []string{
		"CREATE TABLE mig_demo_a (id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(), name text)",
		"ALTER TABLE users ADD COLUMN mig_demo_marker text",
		"CREATE TABLE mig_demo_b (id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(), a_id uuid REFERENCES mig_demo_a(id) ON DELETE CASCADE, user_id uuid REFERENCES users(id))",
		"INSERT INTO mig_demo_a (name) VALUES ('seed-1'), ('seed-2')",
	}, 1000)
	if err != nil {
		t.Fatalf("ExecuteSQLTransaction with developer role: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 statement results, got %d", len(results))
	}

	// Verify the new table is owned by eurobase_migrator (the role
	// switch worked) and not by eurobase_api (the connection's actual
	// session user).
	var owner string
	err = pool.QueryRow(context.Background(),
		`SELECT tableowner FROM pg_tables WHERE schemaname = $1 AND tablename = 'mig_demo_a'`,
		schema,
	).Scan(&owner)
	if err != nil {
		t.Fatalf("read tableowner: %v", err)
	}
	if owner != "eurobase_migrator" {
		t.Errorf("mig_demo_a owner = %q, want %q (the SET LOCAL ROLE didn't take effect for CREATE)", owner, "eurobase_migrator")
	}

	// Cleanup: drop the demo tables and the column we added.
	_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+pgIdent(schema)+".mig_demo_b CASCADE")
	_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+pgIdent(schema)+".mig_demo_a CASCADE")
	_, _ = pool.Exec(context.Background(), "ALTER TABLE "+pgIdent(schema)+".users DROP COLUMN IF EXISTS mig_demo_marker")
}

// pgIdent quotes a Postgres identifier for use in raw SQL. Mirrors the
// engine's quoteIdent helper but available to tests in this file.
func pgIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Compile-time assertion that the test file uses pgx (for Begin signature).
var _ = pgx.ErrNoRows
