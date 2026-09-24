package query

import "testing"

func TestValidateNoPrivilegeStatements(t *testing.T) {
	blocked := []string{
		"GRANT SELECT ON ALL TABLES IN SCHEMA public TO PUBLIC",
		"revoke all on t from x",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO x",
		"SET ROLE eurobase_gateway",
		"set local role x",
		"RESET ROLE",
		"SET SESSION AUTHORIZATION x",
		"RESET ALL",
		"SET search_path TO public",
		"SELECT set_config('role', 'x', true)",
		"CREATE ROLE x",
		"ALTER USER x WITH PASSWORD 'p'",
		"DROP GROUP x",
		"REASSIGN OWNED BY a TO b",
		"DROP OWNED BY a",
		"ALTER TABLE t OWNER TO x",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$",
		"select 1; reset role",
	}
	for _, q := range blocked {
		if err := ValidateNoPrivilegeStatements(q); err == nil {
			t.Errorf("expected rejection: %q", q)
		}
	}

	allowed := []string{
		"SELECT * FROM users WHERE role = 'admin'",
		"SELECT 'GRANT ALL' AS note",
		"-- reset role\nSELECT 1",
		"CREATE TABLE posts (id uuid, owner uuid, title text)",
		"ALTER TABLE users ADD COLUMN nickname text",
		`SELECT "grant" FROM awards`,
		"UPDATE settings SET value = 'x' WHERE key = 'search'",
		"CREATE POLICY p ON posts USING (owner = auth_uid())",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql SECURITY INVOKER AS $$ SELECT 1 $$",
	}
	for _, q := range allowed {
		if err := ValidateNoPrivilegeStatements(q); err != nil {
			t.Errorf("unexpected rejection of %q: %v", q, err)
		}
	}
}
