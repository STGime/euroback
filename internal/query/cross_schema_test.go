package query

import (
	"strings"
	"testing"
)

// Closes advisory GHSA-5cj5-c9f7-9gcj — SDK /v1/db/sql callers must not
// be able to reference any schema other than their own tenant schema.

func TestValidateNoCrossSchemaRefs_AcceptsLegitimateQueries(t *testing.T) {
	allowed := "tenant_abc"
	cases := []struct {
		name string
		sql  string
	}{
		{"no qualifier", "SELECT id, name FROM users WHERE active = true"},
		{"own schema qualifier", "SELECT * FROM tenant_abc.users"},
		{"own schema, mixed case", "SELECT * FROM TENANT_ABC.users"},
		{"alias.column not a schema", "SELECT u.email, u.name FROM users u"},
		{"join with aliases", "SELECT u.id, t.title FROM users u JOIN todos t ON t.user_id = u.id"},
		{"cte and aliased select", "WITH cte AS (SELECT 1 AS x) SELECT cte.x FROM cte"},
		{"function call no schema", "SELECT now(), count(*) FROM events"},
		{"string literal that looks like schema ref", "SELECT 'public.api_keys is private' AS msg"},
		{"line comment containing forbidden ref", "-- public.api_keys\nSELECT 1 FROM events"},
		{"block comment containing forbidden ref", "/* public.api_keys */ SELECT 1 FROM events"},
		{"dollar quoted string containing forbidden ref", "SELECT $$public.api_keys$$ AS s FROM events"},
		{"tagged dollar quoted string", "SELECT $tag$public.api_keys$tag$ AS s FROM events"},
		{"pg_temp ref allowed", "SELECT * FROM pg_temp.tmp_data"},
		{"quoted column name with dot doesn't fool scanner", `SELECT t."weird.col" FROM t`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateNoCrossSchemaRefs(tc.sql, allowed); err != nil {
				t.Errorf("ValidateNoCrossSchemaRefs(%q, %q) = %v, want nil", tc.sql, allowed, err)
			}
		})
	}
}

func TestValidateNoCrossSchemaRefs_RejectsForbiddenSchemas(t *testing.T) {
	allowed := "tenant_abc"
	cases := []struct {
		name string
		sql  string
	}{
		{"public schema", "SELECT * FROM public.api_keys"},
		{"public mixed case", "SELECT * FROM Public.Api_Keys"},
		{"public quoted", `SELECT * FROM "public".api_keys`},
		{"pg_catalog ref", "SELECT * FROM pg_catalog.pg_class"},
		{"information_schema ref", "SELECT * FROM information_schema.tables"},
		{"other tenant", "SELECT * FROM tenant_other_uuid.users"},
		{"other tenant with whitespace before dot", "SELECT * FROM tenant_other  .  users"},
		{"pg_toast ref", "SELECT * FROM pg_toast.foo"},
		{"public function call", "SELECT public.uuid_generate_v4()"},
		{"forbidden ref in CTE", "WITH t AS (SELECT * FROM public.projects) SELECT * FROM t"},
		{"forbidden ref in subquery", "SELECT (SELECT count(*) FROM pg_catalog.pg_class) AS n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNoCrossSchemaRefs(tc.sql, allowed)
			if err == nil {
				t.Fatalf("ValidateNoCrossSchemaRefs(%q, %q) = nil, want non-nil", tc.sql, allowed)
			}
			if !strings.Contains(err.Error(), "not allowed") && !strings.Contains(err.Error(), "schema") {
				t.Errorf("error %v should mention schema/not allowed", err)
			}
		})
	}
}

func TestValidateNoCrossSchemaRefs_AllowedSchemaIsCaseInsensitive(t *testing.T) {
	if err := ValidateNoCrossSchemaRefs("SELECT * FROM tenant_xyz.users", "TENANT_XYZ"); err != nil {
		t.Errorf("case-insensitive match failed: %v", err)
	}
	if err := ValidateNoCrossSchemaRefs("SELECT * FROM TENANT_XYZ.users", "tenant_xyz"); err != nil {
		t.Errorf("case-insensitive match failed (other direction): %v", err)
	}
}

func TestScanIdentifiersAndDots_HandlesQuotedAndUnquoted(t *testing.T) {
	got := scanIdentifiersAndDots(`SELECT "weird name".col FROM "schema-with-dash".rel`)
	// Expect: ident="weird name", dot, ident=col, ident=schema-with-dash, dot, ident=rel
	// ("SELECT" and "FROM" are also idents)
	if len(got) < 6 {
		t.Fatalf("expected at least 6 tokens, got %d: %+v", len(got), got)
	}
	// Find the 'weird name' . col pair.
	foundQuotedRef := false
	foundDashRef := false
	for i := 0; i+2 < len(got); i++ {
		if got[i].kind == tokIdent && got[i].value == "weird name" &&
			got[i+1].kind == tokDot && got[i+2].kind == tokIdent && got[i+2].value == "col" {
			foundQuotedRef = true
		}
		if got[i].kind == tokIdent && got[i].value == "schema-with-dash" &&
			got[i+1].kind == tokDot && got[i+2].kind == tokIdent && got[i+2].value == "rel" {
			foundDashRef = true
		}
	}
	if !foundQuotedRef {
		t.Errorf("did not find quoted-ident.col pair; got %+v", got)
	}
	if !foundDashRef {
		t.Errorf("did not find schema-with-dash.rel pair; got %+v", got)
	}
}

func TestSchemaIsForbidden(t *testing.T) {
	for _, s := range []string{"public", "pg_catalog", "information_schema", "pg_toast", "pg_internal", "tenant_abc", "tenant_xyz"} {
		if !schemaIsForbidden(s) {
			t.Errorf("schemaIsForbidden(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"pg_temp", "myschema", "alpha", "u"} {
		if schemaIsForbidden(s) {
			t.Errorf("schemaIsForbidden(%q) = true, want false", s)
		}
	}
}
