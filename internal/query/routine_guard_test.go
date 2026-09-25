package query

import "testing"

func TestValidateNoRoutineOrSessionObjects(t *testing.T) {
	allow := []string{
		"DELETE FROM events WHERE temp > 30",
		"UPDATE readings SET temp = 20",
		"INSERT INTO logs (msg) VALUES ('DO $$ nothing $$')",
		"CREATE TABLE archive (id int)",
		"CREATE INDEX ON events (created_at)",
		"select aufraeumen_fristen();",
		"SELECT 'set_config' AS label",
		"INSERT INTO temp (v) VALUES (1)",
		"MERGE INTO temp t USING src s ON t.id = s.id WHEN MATCHED THEN DELETE",
		"SELECT local, temp FROM readings",
	}
	for _, s := range allow {
		if err := ValidateNoRoutineOrSessionObjects(s); err != nil {
			t.Errorf("%q: unexpected error %v", s, err)
		}
	}
	deny := []string{
		"DO $$BEGIN NULL; END$$",
		"do language plpgsql $x$ begin null; end $x$",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$",
		"CREATE OR REPLACE PROCEDURE p() LANGUAGE sql AS 'SELECT 1'",
		"CREATE TEMP TABLE t (id int)",
		"CREATE TEMPORARY VIEW v AS SELECT 1",
		"CREATE GLOBAL TEMPORARY TABLE t (id int)",
		"SELECT * INTO TEMP t FROM events",
		"SELECT * FROM pg_temp.t",
		"SELECT set_config('app.x', '1', false)",
		`SELECT "set_config"('app.x', '1', false)`,
	}
	for _, s := range deny {
		if err := ValidateNoRoutineOrSessionObjects(s); err == nil {
			t.Errorf("%q: want error", s)
		}
	}
}
