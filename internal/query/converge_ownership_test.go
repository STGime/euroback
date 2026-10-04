package query

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// converge_tenant_ownership (migration 000136), conservative step 1:
//   - migrator-owned application tables → <schema>_ddl;
//   - gateway-owned tables are NOT reassigned (reassigning would flip RLS
//     for live SDK traffic) — only counted/warned and granted to _func;
//   - _func gets DML on every application table, whatever the owner;
//   - system tables (incl. storage_shared_prefixes) stay migrator-owned.
//
// Needs a DB migrated by scripts/db/apply-migrations.sh (a superuser stand-in,
// so it can create objects as each role):
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run ConvergeTenantOwnership
//
// Not exercised here (admin is a superuser): the developer-owned reassign
// limitation and lock-timeout skip paths.
func TestConvergeTenantOwnership(t *testing.T) {
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
	email := "converge-" + suffix + "@test.eurobase.local"
	schema := "tenant_converge_" + suffix
	var ownerID, projectID string
	if err := pool.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, email).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'Converge', $2, $3, $4, 'fr-par', 'free', 'provisioning') RETURNING id`,
		ownerID, "converge-"+suffix, schema, "b-converge-"+suffix).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id = $1`, ownerID)
		// deprovision_tenant drops the schema, not the per-tenant roles.
		_, _ = pool.Exec(c, fmt.Sprintf(`DROP ROLE IF EXISTS %s_func`, schema))
		_, _ = pool.Exec(c, fmt.Sprintf(`DROP ROLE IF EXISTS %s_ddl`, schema))
	})
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1, 'Converge', 'free')`, projectID); err != nil {
		t.Fatal(err)
	}
	// provision_tenant may set its own schema name; read back the real one.
	if err := pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(fmt.Sprintf(`SET ROLE eurobase_migrator; CREATE TABLE %s.t_migrator (id serial primary key, n text); RESET ROLE`, schema))
	mustExec(fmt.Sprintf(`SET ROLE eurobase_gateway; CREATE TABLE %s.t_gateway (id int primary key); RESET ROLE`, schema))

	if _, err := pool.Exec(ctx, `SELECT converge_tenant_ownership($1)`, schema); err != nil {
		t.Fatalf("converge_tenant_ownership: %v", err)
	}

	ddl := schema + "_ddl"
	fn := schema + "_func"
	owner := func(rel string) string {
		t.Helper()
		var o string
		if err := pool.QueryRow(ctx,
			`SELECT r.rolname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
			 JOIN pg_roles r ON r.oid=c.relowner WHERE n.nspname=$1 AND c.relname=$2`, schema, rel).Scan(&o); err != nil {
			t.Fatalf("owner(%s): %v", rel, err)
		}
		return o
	}

	// migrator-owned table → _ddl.
	if got := owner("t_migrator"); got != ddl {
		t.Errorf("t_migrator owned by %s, want %s", got, ddl)
	}
	// gateway-owned table stays gateway-owned (deferred to the cutover step).
	if got := owner("t_gateway"); got != "eurobase_gateway" {
		t.Errorf("t_gateway owned by %s, want eurobase_gateway (not reassigned in step 1)", got)
	}
	// system tables stay migrator-owned.
	for _, sys := range []string{"users", "storage_shared_prefixes"} {
		if got := owner(sys); got != "eurobase_migrator" {
			t.Errorf("%s owned by %s, want eurobase_migrator", sys, got)
		}
	}

	// _func has DML on both tables (reassigned and not), ready for the cutover.
	for _, tbl := range []string{"t_migrator", "t_gateway"} {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT has_table_privilege($1, $2, 'INSERT')`, fn, schema+"."+tbl).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("%s lacks INSERT on %s.%s after converge", fn, schema, tbl)
		}
	}
	// The migration's grant loop excludes system tables, so it widens
	// nothing on them — covered by the ownership assertions above (users /
	// storage_shared_prefixes stay migrator-owned). _func's existing
	// provisioning grants are out of scope here.

	// Pre-flip readiness gate for SDK_FUNC_LOGIN (step 4). These are the two
	// invariants that must hold before SDK customer SQL moves onto the
	// `_func` login: (1) no application table still owned by the gateway,
	// (2) `_func` has full DML on every application table.
	gwOwned, missingDML, err := TenantFuncReadiness(ctx, pool, schema)
	if err != nil {
		t.Fatalf("TenantFuncReadiness: %v", err)
	}
	// converge granted _func DML on every application table (reassigned and
	// deferred alike), so nothing is missing DML even before the flip.
	if len(missingDML) != 0 {
		t.Errorf("_func missing DML on %v after converge; want none", missingDML)
	}
	// t_gateway is still gateway-owned (step 1 defers the reassign), so the
	// readiness check must FLAG it — this is exactly the condition that must
	// reach zero before the flag flips.
	flagged := false
	for _, n := range gwOwned {
		if n == "t_gateway" {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("readiness did not flag the gateway-owned t_gateway (got %v)", gwOwned)
	}

	// Simulate the pre-flip remediation: reassign the gateway-owned table to
	// _ddl. Readiness then reports clean — safe to flip for this schema.
	mustExec(fmt.Sprintf(`ALTER TABLE %s.t_gateway OWNER TO %s`, schema, ddl))
	gwOwned, missingDML, err = TenantFuncReadiness(ctx, pool, schema)
	if err != nil {
		t.Fatalf("TenantFuncReadiness (post-reassign): %v", err)
	}
	if len(gwOwned) != 0 {
		t.Errorf("gateway still owns %v after reassign; want none", gwOwned)
	}
	if len(missingDML) != 0 {
		t.Errorf("_func missing DML on %v after reassign; want none", missingDML)
	}
}
