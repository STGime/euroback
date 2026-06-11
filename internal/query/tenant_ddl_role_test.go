package query

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Integration tests for the tenant-migration containment model (#190 / PR
// #209). Each migration runs under a PER-TENANT LOGIN role
// (tenant_<id>_ddl, member of nothing) the gateway connects as directly,
// so a body's RESET ROLE lands on that same harmless role and cannot pivot
// into another tenant. These verify the Postgres-enforced boundary the
// regex validator cannot provide.
//
// They skip unless a connection AS a tenant ddl role is provided:
//
//	TEST_DDL_ROLE_DSN  = postgres://tenant_<id>_ddl:<pw>@host/eurobase
//	TEST_TENANT_SCHEMA = tenant_<id>
//	TEST_OTHER_SCHEMA  = some other tenant_<id> (for the pivot test)
//
// The full battery is also reproduced standalone (no app deps) in
// scripts/verify-tenant-migration-isolation.sh, which was run on
// Postgres 16 to verify this design.

func ddlRoleConn(t *testing.T) (*pgx.Conn, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	dsn := os.Getenv("TEST_DDL_ROLE_DSN")
	schema := os.Getenv("TEST_TENANT_SCHEMA")
	if dsn == "" || schema == "" {
		t.Skip("set TEST_DDL_ROLE_DSN + TEST_TENANT_SCHEMA to run tenant-ddl-role integration tests")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect with TEST_DDL_ROLE_DSN: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn, schema
}

// runBody executes sqlText as the connected tenant role with search_path
// pinned, in a rolled-back tx (leaves no state).
func runBody(t *testing.T, conn *pgx.Conn, schema, sqlText string) error {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL search_path TO "`+schema+`"`); err != nil {
		return err
	}
	_, err = conn.PgConn().Exec(ctx, sqlText).ReadAll()
	return err
}

func TestTenantDDLRole_CannotWritePlatformTables(t *testing.T) {
	conn, schema := ddlRoleConn(t)
	if err := runBody(t, conn, schema, "UPDATE public.projects SET plan='pro';"); err == nil {
		t.Fatal("SECURITY: tenant ddl role wrote public.projects")
	}
}

func TestTenantDDLRole_PivotViaResetRoleDenied(t *testing.T) {
	conn, schema := ddlRoleConn(t)
	other := os.Getenv("TEST_OTHER_SCHEMA")
	if other == "" {
		t.Skip("set TEST_OTHER_SCHEMA for the cross-tenant pivot test")
	}
	// The exact PoC: RESET ROLE (→ the connected login role, member of
	// nothing) then SET ROLE into another tenant's ddl role.
	err := runBody(t, conn, schema,
		`DO $$ BEGIN EXECUTE 'RESET ROLE'; EXECUTE 'SET ROLE "`+other+`_ddl"'; END $$;`)
	if err == nil {
		t.Fatal("SECURITY: pivoted into another tenant's ddl role via RESET ROLE")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Logf("denied (non-permission error also acceptable): %v", err)
	}
}

func TestTenantDDLRole_CannotForgeOrReadBookkeeping(t *testing.T) {
	conn, schema := ddlRoleConn(t)
	if err := runBody(t, conn, schema,
		`INSERT INTO public.tenant_migrations(project_id,version,sql,checksum) VALUES (gen_random_uuid(),1,'x','y');`); err == nil {
		t.Fatal("SECURITY: tenant ddl role wrote public.tenant_migrations directly")
	}
	if err := runBody(t, conn, schema, `SELECT count(*) FROM public.tenant_migrations;`); err == nil {
		t.Fatal("SECURITY: tenant ddl role read public.tenant_migrations directly")
	}
}

func TestTenantDDLRole_CanManageOwnSchema(t *testing.T) {
	conn, schema := ddlRoleConn(t)
	if err := runBody(t, conn, schema,
		`CREATE TABLE _eb_probe (id int PRIMARY KEY, note text CHECK (note <> ''));`); err != nil {
		t.Fatalf("legitimate own-schema DDL was rejected: %v", err)
	}
}
