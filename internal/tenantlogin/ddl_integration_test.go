package tenantlogin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDDLLoginIntegration: the persistent `<schema>_ddl` login under the
// production roles. Needs a database built by scripts/db/apply-migrations.sh:
//
//	DDL_LOGIN_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase?sslmode=disable \
//	DDL_LOGIN_TEST_DEV_URL=postgres://eurobase_developer:localdev@localhost:5432/eurobase \
//	go test ./internal/tenantlogin/ -run DDLLoginIntegration
//
// The keeper runs on the developer pool (as in the worker); the test then
// logs in as tenant A's DDL role and checks what SQL running there can and
// cannot reach.
func TestDDLLoginIntegration(t *testing.T) {
	adminURL, devURL := os.Getenv("DDL_LOGIN_TEST_ADMIN_URL"), os.Getenv("DDL_LOGIN_TEST_DEV_URL")
	if adminURL == "" || devURL == "" {
		t.Skip("DDL_LOGIN_TEST_ADMIN_URL / DDL_LOGIN_TEST_DEV_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	dev, err := pgxpool.New(ctx, devURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()

	schemaA := provisionTestTenant(t, admin)
	schemaB := provisionTestTenant(t, admin)

	secret := []byte(strings.Repeat("d", 32))
	database := dev.Config().ConnConfig.Database
	if err := EnsureDDLLogin(ctx, dev, database, secret, schemaA); err != nil {
		t.Fatalf("EnsureDDLLogin: %v", err)
	}

	role := DDLRole(schemaA)
	cfg, err := pgx.ParseConfig(devURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = role, DDLPassword(secret, schemaA)
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("login as %s: %v", role, err)
	}
	defer conn.Close(ctx)
	a := pgx.Identifier{schemaA}.Sanitize()
	b := pgx.Identifier{schemaB}.Sanitize()

	// Allowed: its own schema — DDL, data, and the system tables (RLS still
	// applies there; no permission error is the point).
	for _, q := range []string{
		fmt.Sprintf("CREATE TABLE %s.ddl_probe (id int PRIMARY KEY, note text)", a),
		fmt.Sprintf("INSERT INTO %s.ddl_probe VALUES (1, 'x')", a),
		fmt.Sprintf("ALTER TABLE %s.ddl_probe ADD COLUMN extra int", a),
		fmt.Sprintf("SELECT count(*) FROM %s.users", a),
		fmt.Sprintf("SELECT count(*) FROM %s.storage_objects", a),
		"SELECT public.is_service_role()",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Errorf("allowed %q: %v", q, err)
		}
	}

	// Refused: platform tables, another tenant, role switches, raising its
	// own limits — directly or inside a routine body.
	for _, q := range []string{
		"SELECT count(*) FROM public.platform_users",
		"UPDATE public.platform_users SET display_name = 'x'",
		"DO $$ BEGIN UPDATE public.platform_users SET display_name = 'x'; END $$",
		"DO $$ BEGIN EXECUTE 'UPDATE pub' || 'lic.projects SET name = name'; END $$",
		fmt.Sprintf("SELECT count(*) FROM %s.users", b),
		fmt.Sprintf("CREATE TABLE %s.planted (id int)", b),
		"SET ROLE eurobase_migrator",
		"SET ROLE eurobase_gateway",
		fmt.Sprintf("SET ROLE %s", pgx.Identifier{FuncRole(schemaB)}.Sanitize()),
		"CREATE TABLE public.planted (id int)",
		"ALTER ROLE CURRENT_USER CONNECTION LIMIT -1",
		fmt.Sprintf("ALTER TABLE %s.users ADD COLUMN planted int", a),
	} {
		if _, err := conn.Exec(ctx, q); err == nil {
			t.Errorf("refused %q: it ran", q)
		}
	}

	// A setting the role puts on itself is cleared by the next pass, and
	// the connection limit holds.
	if _, err := conn.Exec(ctx, "ALTER ROLE CURRENT_USER SET work_mem = '1GB'"); err != nil {
		t.Fatalf("self SET (allowed by Postgres): %v", err)
	}
	if err := EnsureDDLLogin(ctx, dev, database, secret, schemaA); err != nil {
		t.Fatalf("second EnsureDDLLogin: %v", err)
	}
	var limit int
	var settings []string
	if err := admin.QueryRow(ctx,
		`SELECT r.rolconnlimit,
		        COALESCE((SELECT array_agg(c) FROM pg_db_role_setting s, unnest(s.setconfig) c WHERE s.setrole = r.oid), '{}')
		   FROM pg_roles r WHERE r.rolname = $1`, role).Scan(&limit, &settings); err != nil {
		t.Fatal(err)
	}
	if limit != DDLConnLimit {
		t.Errorf("connection limit = %d, want %d", limit, DDLConnLimit)
	}
	if len(settings) != 0 {
		t.Errorf("role settings not reset: %v", settings)
	}

	// The keeper never touched tenant B's role.
	var bLogin bool
	if err := admin.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, DDLRole(schemaB)).Scan(&bLogin); err != nil {
		t.Fatal(err)
	}
	if bLogin {
		t.Error("tenant B's DDL role got a login without being ensured")
	}
}

// provisionTestTenant creates a project and runs provision_tenant as the
// admin; returns its schema. Cleaned up after the test (roles included —
// deprovision_tenant drops the schema only).
func provisionTestTenant(t *testing.T, admin *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	suffix := hex.EncodeToString(buf)
	schema := "tenant_dd1e_" + suffix // matches ^tenant_[0-9a-f_]+$
	var ownerID, projectID string
	if err := admin.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, "ddl-"+suffix+"@test.eurobase.local").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'DDL login', $2, $3, $4, 'fr-par', 'free', 'provisioning') RETURNING id`,
		ownerID, "ddl-"+suffix, schema, "b-ddl-"+suffix).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = admin.Exec(c, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = admin.Exec(c, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = admin.Exec(c, `DELETE FROM platform_users WHERE id = $1`, ownerID)
		_, _ = admin.Exec(c, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, pgx.Identifier{FuncRole(schema)}.Sanitize()))
		_, _ = admin.Exec(c, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, pgx.Identifier{DDLRole(schema)}.Sanitize()))
	})
	if _, err := admin.Exec(ctx, `SELECT provision_tenant($1, 'DDL login', 'free')`, projectID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}
