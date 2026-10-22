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

// TestViewGuard_DB runs the rule against real Postgres: every way of
// creating or changing a view that would run as its owner is refused,
// views that apply RLS are allowed, and views that existed before the
// request are left alone.
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run TestViewGuard_DB
func TestViewGuard_DB(t *testing.T) {
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

	schema := fmt.Sprintf("tenant_viewguard_%d", os.Getpid())
	ident := pgx.Identifier{schema}.Sanitize()
	mustExec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec("DROP SCHEMA IF EXISTS " + ident + " CASCADE")
	mustExec("CREATE SCHEMA " + ident)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+ident+" CASCADE") })
	mustExec("CREATE TABLE " + ident + ".secret (id int, note text)")
	mustExec("CREATE VIEW " + ident + ".legacy AS SELECT * FROM " + ident + ".secret")
	mustExec("CREATE VIEW " + ident + ".safe WITH (security_invoker = true) AS SELECT * FROM " + ident + ".secret")

	// run executes sql under the guard in a transaction that is always
	// rolled back, so each case starts from the same schema.
	run := func(sql string) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+ident); err != nil {
			t.Fatal(err)
		}
		return withViewGuard(ctx, tx, schema, func() error {
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
		"DROP VIEW safe",
		// Views that already ran as their owner keep working.
		"CREATE OR REPLACE VIEW legacy AS SELECT * FROM secret",
		"ALTER VIEW legacy RENAME TO legacy2",
		"DROP VIEW legacy",
		"SELECT * FROM legacy",
		"INSERT INTO secret VALUES (1, 'x')",
	}
	for _, sql := range allowed {
		if err := run(sql); err != nil {
			t.Errorf("%s\n  want allowed, got %v", sql, err)
		}
	}

	// The refusal names the view and says how to fix it.
	err = run("CREATE VIEW leaky AS SELECT * FROM secret")
	if err == nil || !strings.Contains(err.Error(), `"leaky"`) || !strings.Contains(err.Error(), "security_invoker = true") {
		t.Errorf("refusal message = %v", err)
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
	if err := withViewGuard(ctx, tx, schema, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var path string
	_ = tx.QueryRow(ctx, "SHOW search_path").Scan(&path)
	_ = tx.Rollback(ctx)
	if !strings.HasPrefix(path, schema) && !strings.HasPrefix(path, ident) {
		t.Errorf("search_path after the guard = %q, want the caller's", path)
	}

	// A view in another schema is not this project's concern.
	other := pgx.Identifier{schema + "_other"}.Sanitize()
	mustExec("CREATE SCHEMA " + other)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+other+" CASCADE") })
	if err := run("CREATE VIEW " + other + ".v AS SELECT 1 AS x"); err != nil {
		t.Errorf("view in another schema: %v", err)
	}
}
