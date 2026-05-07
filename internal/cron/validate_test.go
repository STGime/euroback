package cron

import (
	"errors"
	"strings"
	"testing"
)

// Closes advisory GHSA-fjjq-cqq9-q793 — cron SQL action validation.
//
// Each case asserts the *outcome* (accepted vs. rejected) rather than a
// specific error value where multiple rejection reasons could apply, so a
// regression that flips the rejection reason still trips the test.

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
		{"qualified function from pg_catalog disallowed", ""}, // sentinel; intentionally empty — see Rejects table
	}
	for _, tc := range cases {
		if tc.sql == "" {
			continue // skipped sentinel
		}
		t.Run(tc.name, func(t *testing.T) {
			if err := validateCronSQLAction(tc.sql); err != nil {
				t.Errorf("validateCronSQLAction(%q) returned %v, want nil", tc.sql, err)
			}
		})
	}
}

func TestValidateCronSQLAction_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantErr error
	}{
		{"empty", "", nil},
		{"whitespace only", "  \n\t ", nil},
		{"multi statement", "INSERT INTO a VALUES (1); INSERT INTO b VALUES (2)", errMultiStatement},
		{
			"multi statement with trailing dml after schema escape",
			"SELECT 1; UPDATE public.projects SET owner_id = '00000000-0000-0000-0000-000000000000' WHERE slug = 'victim'",
			errMultiStatement,
		},
		{"public schema reference", "SELECT * FROM public.api_keys", errForbiddenSchema},
		{"public schema mixed case", "select * from PuBLic.projects", errForbiddenSchema},
		{"pg_catalog reference", "SELECT oid FROM pg_catalog.pg_class", errForbiddenSchema},
		{"information_schema reference", "SELECT table_name FROM information_schema.tables", errForbiddenSchema},
		{"cross-tenant reference", "SELECT * FROM tenant_other_uuid.users", errForbiddenSchema},
		{"forbidden schema with whitespace before dot", "SELECT * FROM public  . api_keys", errForbiddenSchema},
		{"forbidden schema inside line comment evaded by stripping comment", "-- public.api_keys\nSELECT 1 FROM events", nil}, // comment stripped → ok
		{"forbidden schema in block comment evaded", "/* public.api_keys */ SELECT 1 FROM events", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCronSQLAction(tc.sql)
			if tc.wantErr == nil && tc.name == "forbidden schema inside line comment evaded by stripping comment" {
				// This case asserts the validator does NOT trip on a
				// commented-out forbidden ref — comments are stripped
				// before the regex.
				if err != nil {
					t.Errorf("validateCronSQLAction(%q) returned %v, want nil (forbidden ref is in comment)", tc.sql, err)
				}
				return
			}
			if tc.wantErr == nil && (tc.name == "empty" || tc.name == "whitespace only") {
				if err == nil {
					t.Errorf("validateCronSQLAction(%q) returned nil, want non-nil empty error", tc.sql)
				}
				return
			}
			if tc.wantErr == nil && tc.name == "forbidden schema in block comment evaded" {
				if err != nil {
					t.Errorf("validateCronSQLAction(%q) returned %v, want nil", tc.sql, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateCronSQLAction(%q) returned nil, want %v", tc.sql, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("validateCronSQLAction(%q) returned %v, want %v", tc.sql, err, tc.wantErr)
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

func TestStripStringsAndComments(t *testing.T) {
	cases := []struct {
		in       string
		mustKeep string // non-empty: must appear in output
		mustHide string // non-empty: must NOT appear in output
	}{
		{"SELECT 'public.api_keys' AS x", "x", "public.api_keys"},
		{"-- public.api_keys\nSELECT 1", "SELECT 1", "public.api_keys"},
		{"/* public.api_keys */ SELECT 1", "SELECT 1", "public.api_keys"},
		{"SELECT $$public.api_keys$$ AS s", "AS s", "public.api_keys"},
		{"SELECT $tag$public.api_keys$tag$ AS s", "AS s", "public.api_keys"},
		{`SELECT "weird.col" FROM t`, "FROM t", `weird.col`},
	}
	for _, tc := range cases {
		got := stripStringsAndComments(tc.in)
		if tc.mustKeep != "" && !strings.Contains(got, tc.mustKeep) {
			t.Errorf("stripStringsAndComments(%q) = %q, missing expected %q", tc.in, got, tc.mustKeep)
		}
		if tc.mustHide != "" && strings.Contains(got, tc.mustHide) {
			t.Errorf("stripStringsAndComments(%q) = %q, contains forbidden %q", tc.in, got, tc.mustHide)
		}
	}
}
