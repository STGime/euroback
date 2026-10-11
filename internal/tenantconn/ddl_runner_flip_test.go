package tenantconn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Step-6e proof. Before PLATFORM_DDL_LOGIN flips, prove — against real
// Postgres — that console/MCP SQL run through the actual tenantconn.DDLRunner
// on a project's own `<schema>_ddl` login keeps working exactly where it
// must, and stays contained where it must.
//
// The flip's whole risk is the migrator-owned system tables (users, …): the
// migrator OWNED them (RLS-bypass), `_ddl` does NOT (RLS-subject). The
// console keeps working only because every system-table policy admits
// public.is_service_role() and the platform path sets app.end_user_role=
// 'service' (query.applyRLSContextTx for KeyType="secret"). This test sets
// that same GUC in the runner's setup callback — exactly what the platform
// path does — and asserts:
//
//   positive : as `_ddl` + service, a system table (users) and an app table
//              owned by `_ddl` are both readable/writable, and session_user
//              really is `<schema>_ddl`;
//   isolation: as `_ddl`, a cross-tenant read of public.platform_users is
//              denied (no grant);
//   negative : as `_ddl` WITHOUT the service GUC, the system table filters to
//              zero rows (RLS is genuinely active — the service branch is
//              load-bearing, not a no-op) while the `_ddl`-owned app table
//              still reads (owner bypass). This is the precise behaviour the
//              flip relies on.
//
// Gated on the same env as TestDDLLoginIntegration so it runs in the same CI
// job (compliance-export.yml):
//
//	DDL_LOGIN_TEST_ADMIN_URL = postgres://postgres:…@localhost:5432/eurobase?sslmode=disable
//	DDL_LOGIN_TEST_DEV_URL   = postgres://eurobase_developer:…@localhost:5432/eurobase?sslmode=require
func TestDDLRunner_ServiceRoleReachesSystemTables(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	adminURL, devURL := os.Getenv("DDL_LOGIN_TEST_ADMIN_URL"), os.Getenv("DDL_LOGIN_TEST_DEV_URL")
	if adminURL == "" || devURL == "" {
		t.Skip("DDL_LOGIN_TEST_ADMIN_URL / DDL_LOGIN_TEST_DEV_URL not set")
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	defer admin.Close()
	dev, err := pgxpool.New(ctx, devURL)
	if err != nil {
		t.Fatalf("dev pool: %v", err)
	}
	defer dev.Close()

	// A secret ≥ 32 bytes, shared by the ensurer (sets the login password)
	// and the runner (derives the same password to connect).
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}

	schema := flipProvisionTenant(t, admin)
	ddlRole := tenantlogin.DDLRole(schema)

	// Set up the persistent `_ddl` login + grants exactly as the worker does
	// (this is what DDL_PERSISTENT_LOGIN enables on the worker — the 6e
	// prerequisite before the gateway flag).
	baseCfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse admin url: %v", err)
	}
	ens, err := tenantlogin.NewDDLEnsurer(dev, baseCfg.Database, secret)
	if err != nil {
		t.Fatalf("new ddl ensurer: %v", err)
	}
	if err := ens.EnsureOne(ctx, schema); err != nil {
		t.Fatalf("ensure ddl login: %v", err)
	}

	// Seed one row in the system table (as the owner, bypassing RLS), and an
	// app table OWNED BY `_ddl` with RLS enabled + a service-role policy —
	// the state 6c convergence leaves an app table in (owned by `_ddl`). The
	// seed statements run as the admin role; `ALTER … OWNER TO <ddl>` sets the
	// ownership the test depends on, so the creating role is immaterial.
	q := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	q(`INSERT INTO "` + schema + `".users (email) VALUES ('seed@test.eurobase.local')`)
	q(`CREATE TABLE "` + schema + `".widgets (id int PRIMARY KEY, note text)`)
	q(`ALTER TABLE "` + schema + `".widgets OWNER TO "` + ddlRole + `"`)
	q(`ALTER TABLE "` + schema + `".widgets ENABLE ROW LEVEL SECURITY`)
	q(`CREATE POLICY widgets_pol ON "` + schema + `".widgets FOR ALL
	     USING (public.is_service_role()) WITH CHECK (public.is_service_role())`)
	q(`INSERT INTO "` + schema + `".widgets (id, note) VALUES (1, 'owned-by-ddl')`)

	// The real runner, built exactly as cmd/gateway does: base = the gateway
	// DSN (user/password overridden per run to the `_ddl` role).
	base, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	runner := NewDDLRunner(base, secret, ens.EnsureOne)
	if runner == nil {
		t.Fatal("NewDDLRunner returned nil")
	}

	// setup mirrors internal/query runDDL for the `_ddl` branch: pin the
	// search_path and apply the RLS context the platform path uses. For a
	// console request (KeyType="secret") that is app.end_user_role='service'.
	serviceSetup := func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL search_path TO "`+schema+`", public`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT set_config('app.end_user_role', 'service', true)`)
		return err
	}
	anonSetup := func(ctx context.Context, tx pgx.Tx) error {
		// No service GUC — this is what the path would set for an
		// unauthenticated/anon caller. Proves RLS is genuinely active.
		_, err := tx.Exec(ctx, `SET LOCAL search_path TO "`+schema+`", public`)
		return err
	}

	// ── positive: as `_ddl` + service, system + app tables both work ──
	if err := runner.Run(ctx, schema, false, serviceSetup, func(ctx context.Context, tx pgx.Tx) error {
		var who string
		if err := tx.QueryRow(ctx, `SELECT session_user`).Scan(&who); err != nil {
			return err
		}
		if who != ddlRole {
			t.Fatalf("session_user = %q, want %q (must be on the per-project _ddl login)", who, ddlRole)
		}
		// System table (migrator-owned, `_ddl` is RLS-subject): the service
		// branch must admit it.
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n < 1 {
			t.Fatalf("service read of system table users returned %d rows, want ≥1 (service branch not admitting _ddl)", n)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users (email) VALUES ('ddl-write@test.eurobase.local')`); err != nil {
			t.Fatalf("service INSERT into system table users failed: %v", err)
		}
		// App table owned by `_ddl` (owner bypass).
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("read of _ddl-owned widgets returned %d rows, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("positive run: %v", err)
	}

	// ── isolation: cross-tenant read of a platform table is denied ──
	err = runner.Run(ctx, schema, true, serviceSetup, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		return tx.QueryRow(ctx, `SELECT count(*) FROM public.platform_users`).Scan(&n)
	})
	if err == nil {
		t.Fatal("SECURITY: _ddl login read public.platform_users across tenants")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("cross-tenant read must be denied with 42501 permission denied, got: %v", err)
	}

	// ── negative: WITHOUT service, the system table filters to zero rows
	//    (RLS active) while the owned app table still reads (owner bypass) ──
	if err := runner.Run(ctx, schema, true, anonSetup, func(ctx context.Context, tx pgx.Tx) error {
		var sys, app int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&sys); err != nil {
			return err
		}
		if sys != 0 {
			t.Fatalf("anon read of system table users returned %d rows, want 0 (RLS must be active for the non-owner _ddl)", sys)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&app); err != nil {
			return err
		}
		if app != 1 {
			t.Fatalf("anon read of _ddl-owned widgets returned %d rows, want 1 (owner bypass)", app)
		}
		return nil
	}); err != nil {
		t.Fatalf("negative run: %v", err)
	}
}

// flipProvisionTenant creates a project + provisions its schema/roles as the
// admin, and tears everything down afterwards. Mirrors the tenantlogin
// package's own provisionTestTenant (not exported).
func flipProvisionTenant(t *testing.T, admin *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	suffix := hex.EncodeToString(buf)
	schema := "tenant_f11p_" + suffix // placeholder; provision_tenant sets the real name
	var ownerID, projectID string
	if err := admin.QueryRow(ctx,
		`INSERT INTO platform_users (email) VALUES ($1) RETURNING id`,
		"flip-"+suffix+"@test.eurobase.local").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'Flip', $2, $3, $4, 'fr-par', 'free', 'provisioning') RETURNING id`,
		ownerID, "flip-"+suffix, schema, "b-flip-"+suffix).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = admin.Exec(c, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = admin.Exec(c, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = admin.Exec(c, `DELETE FROM platform_users WHERE id = $1`, ownerID)
		for _, r := range []string{tenantlogin.FuncRole(schema), tenantlogin.DDLRole(schema)} {
			id := pgx.Identifier{r}.Sanitize()
			_, _ = admin.Exec(c, "DROP OWNED BY "+id)
			_, _ = admin.Exec(c, "DROP ROLE IF EXISTS "+id)
		}
	})
	if _, err := admin.Exec(ctx, `SELECT provision_tenant($1, 'Flip', 'free')`, projectID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	// provision_tenant names the schema from the project id; the _ddl role
	// name tracks it. Return the real schema.
	if !strings.HasPrefix(schema, "tenant_") {
		t.Fatalf("unexpected schema name %q", schema)
	}
	return schema
}
