package cron

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sql / rpc cron actions run as the tenant's own `<schema>_func` login.
// Needs a database built by scripts/db/apply-migrations.sh:
//
//	CRON_TEST_DEV_URL=postgres://eurobase_developer:localdev@localhost:5464/eurobase?sslmode=disable \
//	go test ./internal/cron/ -run TenantLogin
func TestExecutor_RunsAsTenantLogin(t *testing.T) {
	devURL := os.Getenv("CRON_TEST_DEV_URL")
	if devURL == "" {
		t.Skip("CRON_TEST_DEV_URL not set")
	}
	ctx := context.Background()
	dev, err := pgxpool.New(ctx, devURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()

	// Two provisioned tenants (as migrator, like CreateProject).
	provision := func(id string) string {
		t.Helper()
		tx, err := dev.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		steps := []string{
			"SET LOCAL ROLE eurobase_migrator",
			`INSERT INTO platform_users (id, email) VALUES ('` + id + `', '` + id + `@cron.test') ON CONFLICT DO NOTHING`,
			`INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			 VALUES ('` + id + `', '` + id + `', 'cron', 'cron-` + id[:8] + `', 'tmp', 'b-` + id[:8] + `', 'fr-par', 'free', 'provisioning')`,
			`SELECT provision_tenant('` + id + `', 'cron', 'free')`,
		}
		for _, s := range steps {
			if _, err := tx.Exec(ctx, s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return "tenant_" + strings.ReplaceAll(id, "-", "_")
	}
	schemaA := provision(uuid.NewString())
	schemaB := provision(uuid.NewString())

	secret := []byte(strings.Repeat("s", 32))
	ens, err := tenantlogin.NewEnsurer(dev, dev.Config().ConnConfig.Database, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := ens.EnsureOne(ctx, schemaA); err != nil {
		t.Fatalf("EnsureOne: %v", err)
	}

	base, err := pgx.ParseConfig(devURL)
	if err != nil {
		t.Fatal(err)
	}
	e := (&Executor{}).WithTenantLogins(base, secret)

	var who, path string
	if err := e.runInTenantTx(ctx, schemaA, RunAsNone, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT session_user, current_setting('search_path')").Scan(&who, &path)
	}); err != nil {
		t.Fatalf("runInTenantTx: %v", err)
	}
	if who != schemaA+"_func" {
		t.Errorf("session_user = %q, want %q", who, schemaA+"_func")
	}
	if !strings.Contains(path, schemaA) {
		t.Errorf("search_path = %q, want tenant schema", path)
	}

	// run_as: "service" sets the RLS role for the job's transaction only;
	// "none" leaves it unset.
	for _, tc := range []struct{ runAs, want string }{{RunAsService, "service"}, {RunAsNone, ""}} {
		var role string
		if err := e.runInTenantTx(ctx, schemaA, tc.runAs, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT coalesce(current_setting('app.end_user_role', true), '')").Scan(&role)
		}); err != nil {
			t.Fatalf("runInTenantTx(%s): %v", tc.runAs, err)
		}
		if role != tc.want {
			t.Errorf("run_as=%s: app.end_user_role = %q, want %q", tc.runAs, role, tc.want)
		}
	}

	// RLS: a service-only table is visible to run_as=service, not to none.
	setup := []string{
		"CREATE TABLE " + pgx.Identifier{schemaA, "svc_only"}.Sanitize() + " (v int)",
		"INSERT INTO " + pgx.Identifier{schemaA, "svc_only"}.Sanitize() + " VALUES (1)",
		"ALTER TABLE " + pgx.Identifier{schemaA, "svc_only"}.Sanitize() + " ENABLE ROW LEVEL SECURITY",
		"CREATE POLICY svc ON " + pgx.Identifier{schemaA, "svc_only"}.Sanitize() + " USING (public.is_service_role())",
		"GRANT SELECT ON " + pgx.Identifier{schemaA, "svc_only"}.Sanitize() + " TO " + pgx.Identifier{schemaA + "_func"}.Sanitize(),
	}
	tx, err := dev.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE eurobase_migrator"); err != nil {
		t.Fatal(err)
	}
	for _, q := range setup {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		runAs string
		want  int
	}{{RunAsService, 1}, {RunAsNone, 0}} {
		var n int
		if err := e.runInTenantTx(ctx, schemaA, tc.runAs, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM svc_only").Scan(&n)
		}); err != nil {
			t.Fatalf("count as %s: %v", tc.runAs, err)
		}
		if n != tc.want {
			t.Errorf("run_as=%s sees %d rows of a service-only table, want %d", tc.runAs, n, tc.want)
		}
	}

	// DryRun (#645): reports what the job would do, then rolls back.
	res, err := e.DryRun(ctx, schemaA, "sql", "DELETE FROM svc_only", RunAsService)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if res.RowsAffected != 1 || !res.DryRun {
		t.Errorf("DryRun = %+v, want 1 row affected, dry_run", res)
	}
	var still int
	if err := e.runInTenantTx(ctx, schemaA, RunAsService, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM svc_only").Scan(&still)
	}); err != nil {
		t.Fatal(err)
	}
	if still != 1 {
		t.Errorf("after DryRun the table has %d rows, want 1 (rolled back)", still)
	}
	if res, err := e.DryRun(ctx, schemaA, "sql", "DELETE FROM svc_only", RunAsNone); err != nil || res.RowsAffected != 0 {
		t.Errorf("DryRun as none = %+v, %v; want 0 rows", res, err)
	}
	if _, err := e.DryRun(ctx, schemaA, "sql", "GRANT SELECT ON svc_only TO PUBLIC", RunAsService); err == nil {
		t.Error("DryRun accepted a GRANT")
	}

	// The tenant role has no access to another tenant's schema.
	err = e.runInTenantTx(ctx, schemaA, RunAsNone, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT count(*) FROM "+pgx.Identifier{schemaB, "todos"}.Sanitize())
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("cross-tenant read: err = %v, want permission denied", err)
	}

	// The server refuses a second statement on this path.
	err = e.runInTenantTx(ctx, schemaA, RunAsNone, func(ctx context.Context, tx pgx.Tx) error {
		_, err := execExtended(ctx, tx, "SELECT 1; SELECT 2")
		return err
	})
	if err == nil {
		t.Error("execExtended ran two statements, want an error")
	}

	// Not configured => refuse rather than fall back to a shared role.
	if err := (&Executor{}).runInTenantTx(ctx, schemaA, RunAsNone, func(context.Context, pgx.Tx) error { return nil }); err == nil {
		t.Error("runInTenantTx without tenant logins: want error")
	}
}
