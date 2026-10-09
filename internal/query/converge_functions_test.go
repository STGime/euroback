package query

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// converge_tenant_functions (migration 000141), step 6c: runtime-neutral
// convergence. Reassigns to <schema>_ddl ONLY objects whose behaviour does
// not depend on their owner — plain (invoker) functions/procedures,
// security_invoker views, and standalone types. SECURITY DEFINER functions
// and owner-run views are LEFT migrator-owned (they run as their owner, so
// reassigning could change RLS semantics; handled per-object / by step 6d).
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run ConvergeTenantFunctions
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
	// Everything a developer creates via the console runs as migrator.
	q(`SET ROLE eurobase_migrator`)
	q(fmt.Sprintf(`CREATE TABLE %s.notes (id int PRIMARY KEY, body text)`, schema))
	q(fmt.Sprintf(`ALTER TABLE %s.notes OWNER TO %q`, schema, schema+"_ddl"))
	// Reassign candidates (runtime-neutral):
	//   plain fn, plain fn naming a platform table (still neutral), a trigger
	//   fn (plain), a procedure, a security_invoker view, a composite type.
	q(fmt.Sprintf(`CREATE FUNCTION %s.plain_helper() RETURNS int LANGUAGE sql AS 'SELECT 1'`, schema))
	q(fmt.Sprintf(`CREATE FUNCTION %s.plain_platform() RETURNS bigint LANGUAGE sql
		AS 'SELECT count(*) FROM public.platform_users'`, schema))
	q(fmt.Sprintf(`CREATE FUNCTION %s.touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`, schema))
	q(fmt.Sprintf(`CREATE PROCEDURE %s.do_nothing() LANGUAGE sql AS 'SELECT 1'`, schema))
	q(fmt.Sprintf(`CREATE VIEW %s.notes_invoker WITH (security_invoker=true) AS SELECT * FROM %s.notes`, schema, schema))
	q(fmt.Sprintf(`CREATE TYPE %s.pair AS (a int, b int)`, schema))
	// Left migrator-owned:
	//   a SECURITY DEFINER fn, and an owner-run (default) view.
	q(fmt.Sprintf(`CREATE FUNCTION %s.definer_fn() RETURNS bigint LANGUAGE sql SECURITY DEFINER
		SET search_path = %s, public AS 'SELECT count(*) FROM notes'`, schema, schema))
	q(fmt.Sprintf(`CREATE VIEW %s.notes_owner AS SELECT * FROM %s.notes`, schema, schema))
	q(`RESET ROLE`)

	if _, err := pool.Exec(ctx, `SELECT public.converge_tenant_functions($1)`, schema); err != nil {
		t.Fatalf("converge_tenant_functions: %v", err)
	}

	fnOwner := func(name string) string {
		var o string
		if err := pool.QueryRow(ctx,
			`SELECT pg_get_userbyid(p.proowner) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
			 WHERE n.nspname=$1 AND p.proname=$2`, schema, name).Scan(&o); err != nil {
			t.Fatalf("owner of fn %s: %v", name, err)
		}
		return o
	}
	relOwner := func(name string) string {
		var o string
		if err := pool.QueryRow(ctx,
			`SELECT pg_get_userbyid(c.relowner) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
			 WHERE n.nspname=$1 AND c.relname=$2`, schema, name).Scan(&o); err != nil {
			t.Fatalf("owner of rel %s: %v", name, err)
		}
		return o
	}
	typeOwner := func(name string) string {
		var o string
		if err := pool.QueryRow(ctx,
			`SELECT pg_get_userbyid(t.typowner) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
			 WHERE n.nspname=$1 AND t.typname=$2`, schema, name).Scan(&o); err != nil {
			t.Fatalf("owner of type %s: %v", name, err)
		}
		return o
	}
	ddl := schema + "_ddl"

	// Reassigned (runtime-neutral).
	for _, fn := range []string{"plain_helper", "plain_platform", "touch", "do_nothing"} {
		if o := fnOwner(fn); o != ddl {
			t.Errorf("%s owner = %s, want %s (plain → reassigned)", fn, o, ddl)
		}
	}
	if o := relOwner("notes_invoker"); o != ddl {
		t.Errorf("notes_invoker view owner = %s, want %s (security_invoker → reassigned)", o, ddl)
	}
	if o := typeOwner("pair"); o != ddl {
		t.Errorf("type pair owner = %s, want %s", o, ddl)
	}

	// Left migrator-owned (owner-dependent behaviour).
	if o := fnOwner("definer_fn"); o != "eurobase_migrator" {
		t.Errorf("definer_fn owner = %s, want eurobase_migrator (SECURITY DEFINER must NOT be reassigned)", o)
	}
	if o := relOwner("notes_owner"); o != "eurobase_migrator" {
		t.Errorf("notes_owner view owner = %s, want eurobase_migrator (owner-run view must NOT be reassigned)", o)
	}
	if o := fnOwner("auth_uid"); o != "eurobase_migrator" {
		t.Errorf("auth_uid owner = %s, want eurobase_migrator", o)
	}

	// Idempotent: a second run changes nothing and does not error.
	if _, err := pool.Exec(ctx, `SELECT public.converge_tenant_functions($1)`, schema); err != nil {
		t.Fatalf("second converge: %v", err)
	}
	if o := fnOwner("plain_helper"); o != ddl {
		t.Errorf("after re-run, plain_helper owner = %s", o)
	}
}
