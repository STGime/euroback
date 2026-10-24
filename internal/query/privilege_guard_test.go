package query

import (
	"strings"
	"testing"
)

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

// Review follow-ups: tokenizer desync, quoted/indirect forms, and no
// false positives on ordinary UPDATE … SET of role-named columns.
func TestValidateNoPrivilegeStatements_ReviewCases(t *testing.T) {
	blocked := []string{
		`SELECT E'\'', set_config('x','y',true)`,
		`SELECT E'\''; RESET ROLE; SELECT 'x'`,
		`SELECT 1 AS ä$x$; RESET ROLE; SELECT 1 AS ö$x$`,
		`SELECT 1 AS foo$x$; RESET ROLE; SELECT 1 AS bar$x$`,
		`SET "role" TO 'x'`,
		`RESET "role"`,
		`SET "search_path" TO x`,
		`SET U&"search\005fpath" TO x`,
		`SELECT "set_config"('a','b',true)`,
		`SET session_authorization = 'x'`,
		`SET SESSION AUTHORIZATION DEFAULT`,
		`CREATE SCHEMA s AUTHORIZATION x`,
		`UPDATE pg_settings SET setting = 'x' WHERE name = 'y'`,
		`SET role = 'x'`,
		`SET LOCAL ROLE x`,
		`COMMIT`,
		`select 1; commit; select 2`,
		`ROLLBACK`,
		`SAVEPOINT a`,
		`RELEASE SAVEPOINT a`,
		`BEGIN`,
		`START TRANSACTION`,
		`END`,
		`ABORT`,
		`PREPARE TRANSACTION 'x'`,
	}
	for _, q := range blocked {
		if err := ValidateNoPrivilegeStatements(q); err == nil {
			t.Errorf("expected rejection: %q", q)
		}
	}
	allowed := []string{
		`UPDATE t SET role = $1 WHERE id = $2`,
		`UPDATE t SET name = $1, role = $2`,
		`INSERT INTO m (id, role) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET role = EXCLUDED.role`,
		`UPDATE t SET ärole = 1`,
		`SELECT E'it\'s fine' AS s`,
		`SELECT U&'\0041' AS s`,
		`SELECT col$1 FROM t`,
		`SELECT $1::text`,
		`SELECT CASE WHEN a THEN 1 ELSE 0 END FROM t`,
		`UPDATE t SET status = 'end'`,
	}
	for _, q := range allowed {
		if err := ValidateNoPrivilegeStatements(q); err != nil {
			t.Errorf("unexpected rejection of %q: %v", q, err)
		}
	}
}

// Settings that change how string literals are tokenized are refused in
// every form, so the lexical checks and the server always agree.
func TestValidateNoPrivilegeStatements_StringLiteralSettings(t *testing.T) {
	for _, s := range []string{
		"SET standard_conforming_strings = off",
		"SET LOCAL standard_conforming_strings TO off",
		"SET SESSION backslash_quote = on",
		"RESET standard_conforming_strings",
		`SET "standard_conforming_strings" = off`,
		"SET escape_string_warning = off",
		"SELECT current_setting('x'), 1 FROM t WHERE standard_conforming_strings",
	} {
		if err := ValidateNoPrivilegeStatements(s); err == nil {
			t.Errorf("%q: want error", s)
		}
	}
	if err := ValidateNoPrivilegeStatements("SELECT 'standard_conforming_strings' AS doc"); err != nil {
		t.Errorf("string literal mentioning the setting: %v", err)
	}
}

// #661: SECURITY DEFINER gets an explanation and the alternatives, not a
// bare "not allowed".
func TestSecurityDefinerRejectionExplains(t *testing.T) {
	for _, q := range []string{
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$",
		"create or replace function f() returns void language plpgsql security   definer as $$ begin end $$",
		"ALTER FUNCTION f() SECURITY DEFINER",
	} {
		err := ValidateNoPrivilegeStatements(q)
		if err == nil || err.Error() != SecurityDefinerRejection {
			t.Errorf("%q: err = %v, want SecurityDefinerRejection", q, err)
		}
	}
	for _, want := range []string{"SECURITY INVOKER", "is_service_role()", "Run as service role", "service key", SecurityDefinerDocsURL} {
		if !strings.Contains(SecurityDefinerRejection, want) {
			t.Errorf("rejection text lacks %q", want)
		}
	}
	if err := ValidateNoPrivilegeStatements("CREATE FUNCTION f() RETURNS int LANGUAGE sql SECURITY INVOKER AS $$ SELECT 1 $$"); err != nil {
		t.Errorf("SECURITY INVOKER refused: %v", err)
	}
	if err := ValidateTenantMigrationSQL("CREATE FUNCTION f() RETURNS void LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$;"); err == nil || err.Error() != SecurityDefinerRejection {
		t.Errorf("migrations: err = %v, want SecurityDefinerRejection", err)
	}
}
