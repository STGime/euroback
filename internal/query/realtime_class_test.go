package query

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTableIsPublicReadable_RealtimeClass checks the realtime public-table
// classifier (#861) against the exact policy shapes ApplyPolicyPreset
// emits, plus RLS-off and a restrictive-narrowed table. Public means every
// reader (anon included) can SELECT every row, so an owner-less realtime
// event may go to all subscribers.
//
// DB-backed; runs in compliance-export.yml (migrated PG16 has
// public.is_service_role). Skipped without REALTIME_CLASS_TEST_URL /
// TEAM_EXPORT_TEST_ADMIN_URL. The "_RealtimeClass" name keeps it in the
// job's run filter.
func TestTableIsPublicReadable_RealtimeClass(t *testing.T) {
	dsn := os.Getenv("REALTIME_CLASS_TEST_URL")
	if dsn == "" {
		dsn = os.Getenv("TEAM_EXPORT_TEST_ADMIN_URL")
	}
	if dsn == "" {
		t.Skip("REALTIME_CLASS_TEST_URL / TEAM_EXPORT_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	schema := "rtclass_" + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(fmt.Sprintf(`CREATE SCHEMA %q`, schema))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema)) })
	// Schema-local auth helpers the owner/authenticated presets reference;
	// public.is_service_role() comes from the migrations.
	exec(fmt.Sprintf(`CREATE FUNCTION %q.auth_uid() RETURNS uuid LANGUAGE sql AS 'SELECT NULL::uuid'`, schema))
	exec(fmt.Sprintf(`CREATE FUNCTION %q.auth_role() RETURNS text LANGUAGE sql AS $$SELECT 'anon'$$`, schema))

	q := func(tbl string) string { return fmt.Sprintf(`%q.%q`, schema, tbl) }
	srv := "public.is_service_role()"

	// name → (setup SQL, want public)
	type tc struct {
		setup []string
		want  bool
	}
	cases := map[string]tc{
		"owner_access": {[]string{
			`CREATE TABLE ` + q("owner") + ` (id int, user_id uuid)`,
			`ALTER TABLE ` + q("owner") + ` ENABLE ROW LEVEL SECURITY`,
			fmt.Sprintf(`CREATE POLICY owner_select ON %s FOR SELECT USING (%s OR user_id = %q.auth_uid())`, q("owner"), srv, schema),
		}, false},
		"public_read_owner_write": {[]string{
			`CREATE TABLE ` + q("pub") + ` (id int, user_id uuid)`,
			`ALTER TABLE ` + q("pub") + ` ENABLE ROW LEVEL SECURITY`,
			`CREATE POLICY public_select ON ` + q("pub") + ` FOR SELECT USING (true)`,
			fmt.Sprintf(`CREATE POLICY owner_insert ON %s FOR INSERT WITH CHECK (%s OR user_id = %q.auth_uid())`, q("pub"), srv, schema),
		}, true},
		"full_access": {[]string{
			`CREATE TABLE ` + q("full") + ` (id int)`,
			`ALTER TABLE ` + q("full") + ` ENABLE ROW LEVEL SECURITY`,
			`CREATE POLICY allow_all ON ` + q("full") + ` FOR ALL USING (true)`,
		}, true},
		"read_only": {[]string{
			`CREATE TABLE ` + q("ro") + ` (id int)`,
			`ALTER TABLE ` + q("ro") + ` ENABLE ROW LEVEL SECURITY`,
			`CREATE POLICY ro_pub ON ` + q("ro") + ` FOR SELECT USING (true)`,
			fmt.Sprintf(`CREATE POLICY ro_svc ON %s FOR ALL USING (%s)`, q("ro"), srv),
		}, true},
		"service_only": {[]string{
			`CREATE TABLE ` + q("svc") + ` (id int)`,
			`ALTER TABLE ` + q("svc") + ` ENABLE ROW LEVEL SECURITY`,
			fmt.Sprintf(`CREATE POLICY service_only ON %s FOR ALL USING (%s)`, q("svc"), srv),
		}, false},
		"authenticated_read": {[]string{
			`CREATE TABLE ` + q("auth") + ` (id int)`,
			`ALTER TABLE ` + q("auth") + ` ENABLE ROW LEVEL SECURITY`,
			fmt.Sprintf(`CREATE POLICY auth_select ON %s FOR SELECT USING (%s OR %q.auth_role() = 'authenticated')`, q("auth"), srv, schema),
		}, false},
		"rls_disabled": {[]string{
			`CREATE TABLE ` + q("norls") + ` (id int)`,
		}, true},
		"public_select_but_restrictive": {[]string{
			`CREATE TABLE ` + q("restr") + ` (id int, tenant_id uuid)`,
			`ALTER TABLE ` + q("restr") + ` ENABLE ROW LEVEL SECURITY`,
			`CREATE POLICY psel ON ` + q("restr") + ` FOR SELECT USING (true)`,
			fmt.Sprintf(`CREATE POLICY rstr ON %s AS RESTRICTIVE FOR SELECT USING (tenant_id = %q.auth_uid())`, q("restr"), schema),
		}, false},
	}

	tableOf := map[string]string{
		"owner_access": "owner", "public_read_owner_write": "pub", "full_access": "full",
		"read_only": "ro", "service_only": "svc", "authenticated_read": "auth",
		"rls_disabled": "norls", "public_select_but_restrictive": "restr",
	}
	for name, c := range cases {
		for _, sql := range c.setup {
			exec(sql)
		}
		got, err := TableIsPublicReadable(ctx, pool, schema, tableOf[name])
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: public = %v, want %v", name, got, c.want)
		}
	}

	// A missing table is not public (fail closed), no error.
	if got, err := TableIsPublicReadable(ctx, pool, schema, "does_not_exist"); err != nil || got {
		t.Errorf("missing table: got %v err %v, want false nil", got, err)
	}

	// In production the resolver runs on the eurobase_gateway runtime role,
	// not a superuser. The classifier reads only pg_catalog (pg_policy /
	// pg_class / pg_get_expr), which is readable regardless of schema
	// privileges — assert that holds for the gateway role so the feature
	// isn't silently always-fail-closed in prod.
	if gwURL := os.Getenv("EXPORT_TEST_GATEWAY_URL"); gwURL != "" {
		gw, err := pgxpool.New(ctx, gwURL)
		if err != nil {
			t.Fatalf("gateway pool: %v", err)
		}
		defer gw.Close()
		for _, p := range []struct {
			tbl  string
			want bool
		}{{"pub", true}, {"owner", false}, {"svc", false}, {"norls", true}} {
			got, err := TableIsPublicReadable(ctx, gw, schema, p.tbl)
			if err != nil {
				t.Errorf("gateway role, %s: %v", p.tbl, err)
				continue
			}
			if got != p.want {
				t.Errorf("gateway role, %s: public = %v, want %v", p.tbl, got, p.want)
			}
		}
	}
}
