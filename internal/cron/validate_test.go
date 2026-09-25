package cron

import (
	"testing"
)

// Closes advisory GHSA-fjjq-cqq9-q793 — cron SQL action validation.
//
// Each case asserts the *outcome* (accepted vs. rejected) rather than a
// specific error value where multiple rejection reasons could apply, so a
// regression that flips the rejection reason still trips the test.

const testSchema = "tenant_11111111_1111_1111_1111_111111111111"

func TestValidateCronSQLAction_Accepts(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"simple insert", "INSERT INTO events (name) VALUES ('rollup')"},
		{"insert with comment", "-- nightly\nINSERT INTO events (n) VALUES (1)"},
		{"trailing semicolon ok", "DELETE FROM stale_rows WHERE created_at < now() - interval '7 days';"},
		{"select with cte", "WITH t AS (SELECT 1) INSERT INTO events SELECT * FROM t"},
		{"string literal containing forbidden word is fine", "INSERT INTO logs (msg) VALUES ('see public.api_keys for context')"},
		{"forbidden ref in line comment", "-- public.api_keys\nSELECT 1 FROM events"},
		{"forbidden ref in block comment", "/* public.api_keys */ SELECT 1 FROM events"},
		{"own schema qualified", "DELETE FROM " + testSchema + ".sessions WHERE expired"},
		{"allowlisted public helper", "INSERT INTO t (id) VALUES (public.gen_random_uuid())"},
		// Shapes of the SQL cron jobs in production (2026-09-25).
		{"prod: delete orphans", "DELETE FROM users WHERE NOT EXISTS (SELECT 1 FROM profiles WHERE profiles.id::uuid = users.id);"},
		{"prod: delete by status", "DELETE FROM delete_requests WHERE status = 'rejected';"},
		{"prod: call own function", "select aufraeumen_fristen();"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateCronSQLAction(tc.sql, testSchema); err != nil {
				t.Errorf("validateCronSQLAction(%q) returned %v, want nil", tc.sql, err)
			}
		})
	}
}

// Cron SQL must be refused by the same checks as the SQL endpoints.
func TestValidateCronSQLAction_Rejects(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"empty", ""},
		{"whitespace only", "  \n\t "},
		{"multi statement", "INSERT INTO a VALUES (1); INSERT INTO b VALUES (2)"},
		{"public table", "SELECT * FROM public.api_keys"},
		{"public mixed case", "select * from PuBLic.projects"},
		{"public with whitespace before dot", "SELECT * FROM public  . api_keys"},
		{"pg_catalog qualified", "SELECT oid FROM pg_catalog.pg_class"},
		{"information_schema", "SELECT table_name FROM information_schema.tables"},
		{"other tenant", "SELECT * FROM tenant_22222222_2222_2222_2222_222222222222.users"},
		{"other tenant, quoted identifier", `SELECT * FROM "tenant_22222222_2222_2222_2222_222222222222".users`},
		{"other tenant, quoted both parts", `INSERT INTO mine SELECT * FROM "tenant_22222222_2222_2222_2222_222222222222"."users"`},
		{"other tenant, unicode-escaped identifier", `SELECT * FROM U&"tenant_22222222_2222_2222_2222_222222222222".users`},
		{"bare catalog view", "SELECT query FROM pg_stat_activity"},
		{"catalog function", "SELECT * FROM pg_stat_get_activity(NULL)"},
		{"grant", "GRANT SELECT ON ALL TABLES IN SCHEMA " + testSchema + " TO PUBLIC"},
		{"revoke", "REVOKE ALL ON events FROM PUBLIC"},
		{"set role", "SET ROLE eurobase_developer"},
		{"reset role", "RESET ROLE"},
		{"set search_path", "SET search_path TO tenant_22222222_2222_2222_2222_222222222222"},
		{"create role", "CREATE ROLE x LOGIN"},
		{"security definer", "CREATE FUNCTION f() RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateCronSQLAction(tc.sql, testSchema); err == nil {
				t.Errorf("validateCronSQLAction(%q) returned nil, want an error", tc.sql)
			}
		})
	}
}

func TestValidateCronRPCName(t *testing.T) {
	good := []string{"rollup_daily", "_internal", "fn1", "send_emails"}
	bad := []string{"", "1starts_with_digit", "has space", "has-dash", "has.dot", "has;semicolon", "DROP TABLE x", "with(parens)"}

	for _, n := range good {
		if err := validateCronRPCName(n); err != nil {
			t.Errorf("validateCronRPCName(%q) returned %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if err := validateCronRPCName(n); err == nil {
			t.Errorf("validateCronRPCName(%q) returned nil, want error", n)
		}
	}
}
