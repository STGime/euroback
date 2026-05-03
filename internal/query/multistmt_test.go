package query

import "testing"

func TestHasMultipleStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"empty", "", false},
		{"single select", "SELECT 1", false},
		{"single select trailing semicolon", "SELECT 1;", false},
		{"single select trailing whitespace and semicolon", "SELECT 1;\n", false},
		{"two selects", "SELECT 1; SELECT 2", true},
		{"two selects both terminated", "SELECT 1; SELECT 2;", true},
		{"semicolon inside single quote", "SELECT ';'", false},
		{"semicolon inside escaped single quote", "SELECT 'it''s; ok'", false},
		{"semicolon inside double-quoted identifier", `SELECT 1 AS "a;b"`, false},
		{"semicolon inside line comment", "SELECT 1 -- x; y\n", false},
		{"line comment then second statement", "SELECT 1; -- comment\nSELECT 2", true},
		{"block comment with semicolon", "SELECT 1 /* a; b */ FROM t", false},
		{"nested block comment", "SELECT 1 /* /* x; */ */ FROM t", false},
		{"block comment then second statement", "SELECT 1 /* hi */; SELECT 2", true},
		{"dollar-quoted body with semicolons", "CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; PERFORM 2; END; $$ LANGUAGE plpgsql", false},
		{"dollar-quoted with tag", "DO $body$ BEGIN PERFORM 1; PERFORM 2; END $body$", false},
		{"function then second statement", "CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql; SELECT f()", true},
		{"BEGIN ... COMMIT migration script", "BEGIN; CREATE TABLE t (id int); INSERT INTO t VALUES (1); COMMIT;", true},
		{"stray dollar sign", "SELECT 'a$b'", false},
		{"unterminated dollar quote", "SELECT $$ unterminated; second", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HasMultipleStatements(tc.sql)
			if got != tc.want {
				t.Errorf("HasMultipleStatements(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}
