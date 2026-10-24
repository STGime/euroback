package query

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSecurityInvokerOn(t *testing.T) {
	for _, tc := range []struct {
		opts []string
		want bool
	}{
		{nil, false},
		{[]string{"check_option=local"}, false},
		{[]string{"security_invoker=true"}, true},
		{[]string{"security_invoker=TRUE"}, true},
		{[]string{"security_invoker=t"}, true},
		{[]string{"security_invoker=yes"}, true},
		{[]string{"security_invoker=y"}, true},
		{[]string{"security_invoker=on"}, true},
		{[]string{"security_invoker=1"}, true},
		{[]string{"security_invoker=false"}, false},
		{[]string{"security_invoker=off"}, false},
		{[]string{"security_invoker=0"}, false},
		{[]string{"security_invoker=no"}, false},
		{[]string{"security_invoker="}, false},
		{[]string{"security_invoker=true", "security_invoker=false"}, false},
		{[]string{"security_barrier=true"}, false},
	} {
		if got := securityInvokerOn(tc.opts); got != tc.want {
			t.Errorf("securityInvokerOn(%q) = %v, want %v", tc.opts, got, tc.want)
		}
	}
}

// TestOwnerRightsGuard_DB runs the rule against real Postgres: every way of
// creating or changing a view that would run as its owner is refused,
// views that apply RLS are allowed, and views that existed before the
// request are left alone.
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run TestOwnerRightsGuard_DB
func TestOwnerRightsGuard_DB(t *testing.T) {
	adminURL := os.Getenv("CONVERGE_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("CONVERGE_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // registered first, so it runs after the other cleanups

	schema := fmt.Sprintf("tenant_ownerguard_%d", os.Getpid())
	ident := pgx.Identifier{schema}.Sanitize()
	mustExec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var mustExecAs func(q string) // set once the owner role exists
	// Everything runs as an ordinary role that owns the schema and its
	// tables, like a project's own login — not as a superuser.
	owner := pgx.Identifier{schema + "_owner"}.Sanitize()
	mustExec("DROP SCHEMA IF EXISTS " + ident + " CASCADE")
	mustExec("DROP ROLE IF EXISTS " + owner)
	mustExec("CREATE ROLE " + owner + " NOLOGIN")
	mustExec("CREATE SCHEMA " + ident + " AUTHORIZATION " + owner)
	t.Cleanup(func() {
		for _, q := range []string{"DROP SCHEMA IF EXISTS " + ident + " CASCADE", "DROP ROLE IF EXISTS " + owner} {
			if _, err := pool.Exec(context.Background(), q); err != nil {
				t.Logf("cleanup %s: %v", q, err)
			}
		}
	})
	mustExecAs = func(q string) {
		t.Helper()
		mustExec("SET ROLE " + owner + "; " + q + "; RESET ROLE")
	}
	mustExecAs("CREATE TABLE " + ident + ".secret (id int, note text)")
	mustExecAs("CREATE VIEW " + ident + ".legacy AS SELECT * FROM " + ident + ".secret")
	mustExecAs("CREATE VIEW " + ident + ".safe WITH (security_invoker = true) AS SELECT * FROM " + ident + ".secret")
	mustExecAs("CREATE TABLE " + ident + ".audit (id int, note text)")
	// Already there before the request: a definer function, an invoker
	// function and a rule.
	mustExecAs("CREATE FUNCTION " + ident + ".legacy_definer() RETURNS bigint LANGUAGE sql SECURITY DEFINER AS 'SELECT 1::bigint'")
	mustExecAs("CREATE FUNCTION " + ident + ".plain() RETURNS bigint LANGUAGE sql AS 'SELECT 1::bigint'")
	mustExecAs("CREATE RULE legacy_rule AS ON INSERT TO " + ident + ".audit DO ALSO NOTIFY audit")
	mustExecAs("CREATE FUNCTION " + ident + ".atomic_definer() RETURNS bigint LANGUAGE sql SECURITY DEFINER BEGIN ATOMIC SELECT 1::bigint; END")
	mustExecAs("CREATE RULE legacy_off AS ON UPDATE TO " + ident + ".audit DO ALSO NOTIFY audit")
	mustExecAs("ALTER TABLE " + ident + ".audit DISABLE RULE legacy_off")

	// run executes sql under the guard in a transaction that is always
	// rolled back, so each case starts from the same schema.
	run := func(sql string) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+ident+"; SET LOCAL ROLE "+owner); err != nil {
			t.Fatal(err)
		}
		return withOwnerRightsGuard(ctx, tx, schema, func() error {
			_, err := tx.Exec(ctx, sql)
			return err
		})
	}

	refused := []string{
		"CREATE VIEW v AS SELECT * FROM secret",
		"CREATE OR REPLACE VIEW v AS SELECT * FROM secret",
		"CREATE VIEW v WITH (security_invoker = false) AS SELECT * FROM secret",
		"CREATE VIEW v WITH (security_invoker = 0) AS SELECT * FROM secret",
		"CREATE VIEW v WITH (security_invoker = 'off') AS SELECT * FROM secret",
		"CREATE VIEW v WITH (security_barrier) AS SELECT * FROM secret",
		"CREATE RECURSIVE VIEW v (id) AS SELECT 1 UNION ALL SELECT id + 1 FROM v WHERE id < 2",
		"ALTER VIEW safe SET (security_invoker = false)",
		"ALTER VIEW safe RESET (security_invoker)",
		// CREATE OR REPLACE without WITH clears the view's options.
		"CREATE OR REPLACE VIEW safe AS SELECT * FROM secret",
		"CREATE MATERIALIZED VIEW m AS SELECT * FROM secret",
		"CREATE MATERIALIZED VIEW m WITH (fillfactor = 70) AS SELECT * FROM secret WITH NO DATA",
		// Not visible to a check of the SQL text.
		"DO $$ BEGIN EXECUTE 'CREATE VIEW v AS SELECT * FROM secret'; END $$",
		"DO $$ BEGIN BEGIN EXECUTE 'CREATE VIEW v AS SELECT * FROM secret'; EXCEPTION WHEN OTHERS THEN NULL; END; END $$",
		// Blinding the catalog query: search_path changed inside a DO
		// block so the schema's own "=" operator wins over pg_catalog's.
		`DO $$ BEGIN PERFORM set_config('search_path', current_schema() || ', pg_catalog', true); END $$;
		 CREATE FUNCTION oid_never_eq(oid, oid) RETURNS boolean LANGUAGE sql AS 'SELECT false';
		 CREATE OPERATOR = (LEFTARG = oid, RIGHTARG = oid, FUNCTION = oid_never_eq);
		 CREATE FUNCTION char_never_eq("char", "char") RETURNS boolean LANGUAGE sql AS 'SELECT false';
		 CREATE OPERATOR = (LEFTARG = "char", RIGHTARG = "char", FUNCTION = char_never_eq);
		 CREATE VIEW v AS SELECT * FROM secret`,
		// SECURITY DEFINER functions and procedures, however they are made.
		"CREATE FUNCTION peek() RETURNS SETOF secret LANGUAGE sql SECURITY DEFINER AS 'SELECT * FROM secret'",
		"CREATE PROCEDURE p() LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'",
		"DO $$ BEGIN EXECUTE 'CREATE FUNCTION peek() RETURNS SETOF secret LANGUAGE sql SECURITY DEFINER AS ''SELECT * FROM secret'''; END $$",
		"ALTER FUNCTION plain() SECURITY DEFINER",
		"CREATE OR REPLACE FUNCTION plain() RETURNS bigint LANGUAGE sql SECURITY DEFINER AS 'SELECT 1::bigint'",
		// Editing an existing definer function.
		"CREATE OR REPLACE FUNCTION legacy_definer() RETURNS bigint LANGUAGE sql SECURITY DEFINER AS 'SELECT count(*) FROM secret'",
		"ALTER FUNCTION legacy_definer() SET work_mem = '1MB'",
		// Rules on tables and views.
		"CREATE RULE leak AS ON INSERT TO audit DO ALSO INSERT INTO audit SELECT * FROM secret",
		"CREATE RULE ins AS ON INSERT TO safe DO INSTEAD INSERT INTO secret VALUES (NEW.id, NEW.note)",
		"DO $$ BEGIN EXECUTE 'CREATE RULE leak AS ON INSERT TO audit DO ALSO INSERT INTO audit SELECT * FROM secret'; END $$",
		"CREATE OR REPLACE RULE legacy_rule AS ON INSERT TO audit DO ALSO INSERT INTO audit SELECT * FROM secret",
		// Editing an existing owner-run view, or turning an existing rule
		// on or into an INSTEAD rule.
		"CREATE OR REPLACE VIEW legacy AS SELECT * FROM secret WHERE id > 0",
		"ALTER TABLE audit ENABLE RULE legacy_off",
		"CREATE OR REPLACE RULE legacy_rule AS ON INSERT TO audit DO INSTEAD NOTIFY audit",
		// Editing an existing BEGIN ATOMIC definer function.
		"CREATE OR REPLACE FUNCTION atomic_definer() RETURNS bigint LANGUAGE sql SECURITY DEFINER BEGIN ATOMIC SELECT count(*) FROM secret; END",
		// Replacing an existing owner-run view with a new one.
		"DROP VIEW legacy; CREATE VIEW legacy AS SELECT * FROM secret",
	}
	for _, sql := range refused {
		err := run(sql)
		if err == nil || !IsRefused(err) {
			t.Errorf("%s\n  want refused, got %v", sql, err)
		}
	}

	allowed := []string{
		"CREATE VIEW v WITH (security_invoker = true) AS SELECT * FROM secret",
		"CREATE VIEW v WITH (security_invoker) AS SELECT * FROM secret",
		"CREATE VIEW v WITH (security_invoker = on, security_barrier = true) AS SELECT * FROM secret",
		"CREATE VIEW v WITH (security_invoker = 'yes') AS SELECT * FROM secret",
		"CREATE VIEW v AS SELECT 1 AS x; ALTER VIEW v SET (security_invoker = true)",
		"ALTER VIEW safe SET (security_barrier = true)",
		"CREATE OR REPLACE VIEW safe WITH (security_invoker = true) AS SELECT * FROM secret",
		"ALTER VIEW safe RENAME COLUMN note TO body",
		"COMMENT ON VIEW safe IS 'x'",
		"ALTER TABLE secret ADD COLUMN extra int",
		"DROP VIEW safe",
		// Views that already ran as their owner keep working.
		"ALTER VIEW legacy RENAME TO legacy2",
		"ALTER TABLE secret RENAME COLUMN note TO body",
		"COMMENT ON VIEW legacy IS 'x'",
		"DROP VIEW legacy",
		"SELECT * FROM legacy",
		// Plain functions and triggers are fine; existing definer
		// functions and rules keep working unchanged.
		"CREATE FUNCTION f() RETURNS bigint LANGUAGE sql AS 'SELECT count(*) FROM secret'",
		"CREATE OR REPLACE FUNCTION plain() RETURNS bigint LANGUAGE sql SECURITY INVOKER AS 'SELECT 2::bigint'",
		"CREATE FUNCTION trg() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'; CREATE TRIGGER t BEFORE INSERT ON secret FOR EACH ROW EXECUTE FUNCTION trg()",
		"SELECT legacy_definer()",
		"INSERT INTO audit VALUES (1, 'x')",
		"ALTER FUNCTION legacy_definer() SECURITY INVOKER",
		"DROP FUNCTION legacy_definer()",
		"DROP RULE legacy_rule ON audit",
		"ALTER TABLE audit DISABLE RULE legacy_rule",
		"INSERT INTO secret VALUES (1, 'x')",
	}
	for _, sql := range allowed {
		if err := run(sql); err != nil {
			t.Errorf("%s\n  want allowed, got %v", sql, err)
		}
	}

	// The refusal names the view and says how to fix it.
	err = run("CREATE VIEW leaky AS SELECT * FROM secret")
	if err == nil || !strings.Contains(err.Error(), `"leaky"`) || !strings.Contains(err.Error(), "security_invoker = true") || !strings.Contains(err.Error(), "Repeat the WITH clause") {
		t.Errorf("refusal message = %v", err)
	}
	err = run("CREATE FUNCTION peek() RETURNS bigint LANGUAGE sql SECURITY DEFINER AS 'SELECT 1::bigint'")
	if err == nil || !strings.Contains(err.Error(), `"peek"`) || !strings.Contains(err.Error(), "SECURITY DEFINER") {
		t.Errorf("definer refusal message = %v", err)
	}
	err = run("CREATE RULE leak AS ON INSERT TO audit DO ALSO NOTIFY x")
	if err == nil || !strings.Contains(err.Error(), `"leak on audit"`) || !strings.Contains(err.Error(), "trigger") {
		t.Errorf("rule refusal message = %v", err)
	}
	err = run("CREATE MATERIALIZED VIEW stats AS SELECT count(*) FROM secret")
	if err == nil || !strings.Contains(err.Error(), `"stats"`) || !strings.Contains(err.Error(), "materialized") {
		t.Errorf("materialized refusal message = %v", err)
	}

	// The guard restores the developer's search_path.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+ident+", public"); err != nil {
		t.Fatal(err)
	}
	// Developer SQL inside the guard changes the path (as a DO block
	// could); the guard must hand back the path the SQL left behind.
	if err := withOwnerRightsGuard(ctx, tx, schema, func() error {
		_, err := tx.Exec(ctx, "DO $$ BEGIN PERFORM set_config('search_path', '"+strings.ReplaceAll(ident, "'", "''")+`, "$user", public', true); END $$`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var path string
	_ = tx.QueryRow(ctx, "SHOW search_path").Scan(&path)
	_ = tx.Rollback(ctx)
	if path != schema+`, "$user", public` && path != ident+`, "$user", public` {
		t.Errorf("search_path after the guard = %q, want the one the SQL set", path)
	}

	// A view in another schema is not this project's concern.
	other := pgx.Identifier{schema + "_other"}.Sanitize()
	mustExec("CREATE SCHEMA " + other + " AUTHORIZATION " + owner)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+other+" CASCADE") })
	if err := run("CREATE VIEW " + other + ".v AS SELECT 1 AS x"); err != nil {
		t.Errorf("view in another schema: %v", err)
	}
}
