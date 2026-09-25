package cron

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/tenantlogin"
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
	schemaA := provision("aaaaaaaa-0000-4000-8000-00000000c0a1")
	schemaB := provision("bbbbbbbb-0000-4000-8000-00000000c0b2")

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
	if err := e.runInTenantTx(ctx, schemaA, func(tx pgx.Tx) error {
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

	// The tenant role has no access to another tenant's schema.
	err = e.runInTenantTx(ctx, schemaA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT count(*) FROM "+pgx.Identifier{schemaB, "todos"}.Sanitize())
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("cross-tenant read: err = %v, want permission denied", err)
	}

	// Not configured => refuse rather than fall back to a shared role.
	if err := (&Executor{}).runInTenantTx(ctx, schemaA, func(pgx.Tx) error { return nil }); err == nil {
		t.Error("runInTenantTx without tenant logins: want error")
	}
}
