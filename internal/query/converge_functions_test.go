package query

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// converge_tenant_functions (migration 000141), step 6c for functions:
//
//   - grants <schema>_ddl the in-schema superset (system-table DML, public
//     helper EXECUTE) FIRST;
//
//   - reassigns migrator-owned functions/views/types to _ddl ONLY when their
//     body stays inside their own schema + public helpers;
//
//   - leaves anything that reaches a platform table / another tenant / a
//     privileged catalog migrator-owned (never silently broken).
//
//     CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//     go test ./internal/query/ -run ConvergeTenantFunctions
func TestConvergeTenantFunctions(t *testing.T) {
	adminURL := os.Getenv("CONVERGE_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("CONVERGE_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", os.Getpid())
	email := "convfn-" + suffix + "@test.eurobase.local"
	schema := "tenant_convfn_" + suffix
	var ownerID, projectID string
	if err := pool.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, email).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'ConvFn', $2, $3, $4, 'fr-par', 'free', 'provisioning') RETURNING id`,
		ownerID, "convfn-"+suffix, schema, "b-convfn-"+suffix).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id = $1`, ownerID)
		for _, r := range []string{schema + "_func", schema + "_ddl"} {
			_, _ = pool.Exec(c, fmt.Sprintf(`DROP OWNED BY %q CASCADE`, r))
			_, _ = pool.Exec(c, fmt.Sprintf(`DROP ROLE IF EXISTS %q`, r))
		}
	})
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1, 'ConvFn', 'free')`, projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	q := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s\n  -> %v", sql, err)
		}
	}
	// Everything a developer would create via the console runs as migrator.
	q(`SET ROLE eurobase_migrator`)
	// An app table owned by _ddl (as a real one would be after 000136).
	q(fmt.Sprintf(`CREATE TABLE %s.notes (id int PRIMARY KEY, body text)`, schema))
	q(fmt.Sprintf(`ALTER TABLE %s.notes OWNER TO %q`, schema, schema+"_ddl"))
	// (a) benign SECURITY DEFINER fn touching its own table.
	q(fmt.Sprintf(`CREATE FUNCTION %s.note_count() RETURNS bigint LANGUAGE sql SECURITY DEFINER
		SET search_path = %s, public AS 'SELECT count(*) FROM notes'`, schema, schema))
	// (b) benign SECURITY DEFINER fn touching a SYSTEM table (users).
	q(fmt.Sprintf(`CREATE FUNCTION %s.user_count() RETURNS bigint LANGUAGE sql SECURITY DEFINER
		SET search_path = %s, public AS 'SELECT count(*) FROM users'`, schema, schema))
	// (c) benign plain (non-definer) fn.
	q(fmt.Sprintf(`CREATE FUNCTION %s.plain_helper() RETURNS int LANGUAGE sql AS 'SELECT 1'`, schema))
	// (d) probe: references a platform table.
	q(fmt.Sprintf(`CREATE FUNCTION %s.read_platform() RETURNS bigint LANGUAGE sql SECURITY DEFINER
		AS 'SELECT count(*) FROM public.platform_users'`, schema))
	// (e) probe: references pg_stat_activity.
	q(fmt.Sprintf(`CREATE FUNCTION %s.peek() RETURNS bigint LANGUAGE sql SECURITY DEFINER
		AS 'SELECT count(*) FROM pg_stat_activity'`, schema))
	// (f) probe: references another tenant schema.
	q(fmt.Sprintf(`CREATE FUNCTION %s.cross_tenant() RETURNS text LANGUAGE plpgsql SECURITY DEFINER
		AS $$ BEGIN RETURN 'tenant_00000000_0000_0000_0000_000000000000.secrets'; END $$`, schema))
	q(`RESET ROLE`)

	// Converge.
	if _, err := pool.Exec(ctx, `SELECT public.converge_tenant_functions($1)`, schema); err != nil {
		t.Fatalf("converge_tenant_functions: %v", err)
	}

	ownerOf := func(fn string) string {
		var owner string
		if err := pool.QueryRow(ctx,
			`SELECT pg_get_userbyid(p.proowner) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
			 WHERE n.nspname=$1 AND p.proname=$2`, schema, fn).Scan(&owner); err != nil {
			t.Fatalf("owner of %s: %v", fn, err)
		}
		return owner
	}
	ddl := schema + "_ddl"
	// Benign ones reassigned.
	for _, fn := range []string{"note_count", "user_count", "plain_helper"} {
		if o := ownerOf(fn); o != ddl {
			t.Errorf("%s owner = %s, want %s (should have been reassigned)", fn, o, ddl)
		}
	}
	// Probe-class left migrator-owned.
	for _, fn := range []string{"read_platform", "peek", "cross_tenant"} {
		if o := ownerOf(fn); o != "eurobase_migrator" {
			t.Errorf("%s owner = %s, want eurobase_migrator (must NOT be reassigned)", fn, o)
		}
	}
	// Platform helpers untouched.
	if o := ownerOf("auth_uid"); o != "eurobase_migrator" {
		t.Errorf("auth_uid owner = %s, want eurobase_migrator", o)
	}

	// The reassigned SECURITY DEFINER functions still RUN (as _ddl): the one
	// touching its own table and the one touching a system table. If _ddl
	// lacked the grant, these would raise permission denied.
	var n int64
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT %s.note_count()`, schema)).Scan(&n); err != nil {
		t.Errorf("note_count() after reassign: %v", err)
	}
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT %s.user_count()`, schema)).Scan(&n); err != nil {
		t.Errorf("user_count() after reassign (runs as _ddl; needs the system-table grant): %v", err)
	}

	// _ddl got the superset: DML on a system table.
	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		var ok bool
		if err := pool.QueryRow(ctx,
			`SELECT has_table_privilege($1, ($2||'.users')::regclass, $3)`, ddl, schema, priv).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("_ddl missing %s on users after converge", priv)
		}
	}
}
