package query

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// converge_tenant_ownership (migration 000136) must give every application
// table / sequence in a tenant schema to <schema>_ddl, whatever role
// created it, and keep the gateway + _func DML on them. Needs a DB migrated
// by scripts/db/apply-migrations.sh (a superuser stand-in, so it can create
// objects as each role):
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run ConvergeTenantOwnership
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
	})
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1, 'Converge', 'free')`, projectID); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// A table owned by each platform role that might have created one, plus
	// a serial (owned sequence) and a view.
	mustExec(fmt.Sprintf(`SET ROLE eurobase_migrator; CREATE TABLE %s.t_migrator (id serial primary key, n text); RESET ROLE`, schema))
	mustExec(fmt.Sprintf(`SET ROLE eurobase_gateway; CREATE TABLE %s.t_gateway (id int primary key); RESET ROLE`, schema))
	mustExec(fmt.Sprintf(`SET ROLE eurobase_migrator; CREATE VIEW %s.v_migrator AS SELECT 1 AS one; RESET ROLE`, schema))

	if _, err := pool.Exec(ctx, `SELECT converge_tenant_ownership($1)`, schema); err != nil {
		t.Fatalf("converge_tenant_ownership: %v", err)
	}

	ddl := schema + "_ddl"
	fn := schema + "_func"

	// Every application relation is now _ddl-owned.
	rows, err := pool.Query(ctx,
		`SELECT c.relname, r.rolname
		 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 JOIN pg_roles r ON r.oid = c.relowner
		 WHERE n.nspname = $1 AND c.relkind IN ('r','S','v')
		   AND c.relname IN ('t_migrator','t_gateway','v_migrator','t_migrator_id_seq')`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var rel, owner string
		if err := rows.Scan(&rel, &owner); err != nil {
			t.Fatal(err)
		}
		seen++
		if owner != ddl {
			t.Errorf("%s owned by %s, want %s", rel, owner, ddl)
		}
	}
	if seen < 3 {
		t.Fatalf("expected the test relations present, saw %d", seen)
	}

	// System tables stay migrator-owned.
	var usersOwner string
	if err := pool.QueryRow(ctx,
		`SELECT r.rolname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		 JOIN pg_roles r ON r.oid=c.relowner WHERE n.nspname=$1 AND c.relname='users'`, schema).Scan(&usersOwner); err != nil {
		t.Fatal(err)
	}
	if usersOwner != "eurobase_migrator" {
		t.Errorf("users owned by %s, want eurobase_migrator", usersOwner)
	}

	// gateway and _func keep DML on the reassigned gateway-created table.
	for _, role := range []string{"eurobase_gateway", fn} {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT has_table_privilege($1, $2, 'INSERT')`, role, schema+".t_gateway").Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("%s lacks INSERT on %s.t_gateway after converge", role, schema)
		}
	}
}
