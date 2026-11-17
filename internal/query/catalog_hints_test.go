package query

import (
	"strings"
	"testing"
)

// A refused catalog read that a developer (or their coding agent) uses to
// inspect their own schema names the tool that answers it instead.
func TestCatalogRefusal_Hints(t *testing.T) {
	cases := []struct {
		name, sql, hint string
	}{
		{"function source", "SELECT pg_get_functiondef(p.oid) FROM pg_proc p", "getDbFunction"},
		{"function arguments", "SELECT pg_get_function_arguments(1)", "getDbFunction"},
		{"policies view", "SELECT * FROM pg_policies WHERE tablename = 'todos'", "/policies"},
		{"trigger definition", "SELECT pg_get_triggerdef(1)", "describeTable"},
		{"constraints", "SELECT pg_get_constraintdef(oid) FROM pg_constraint", "describeTable"},
		{"rls flag", "SELECT relrowsecurity FROM pg_class", "describeTable"},
		{"views", "SELECT * FROM pg_views", "listSchemaObjects"},
		{"roles", "SELECT rolname FROM pg_roles", "rls-audit"},
		{"qualified catalog", "SELECT * FROM pg_catalog.pg_proc", "getDbFunction"},
		{"information_schema columns", "SELECT column_name FROM information_schema.columns", "describeTable"},
		{"information_schema grants", "SELECT * FROM information_schema.role_table_grants", "rls-audit"},
		{"information_schema routines", "SELECT * FROM information_schema.routines", "getDbFunction"},
	}
	for _, tc := range cases {
		err := ValidateNoCrossSchemaRefs(tc.sql, "tenant_x")
		if err == nil {
			err = ValidateNoCatalogRefs(tc.sql)
		}
		if err == nil {
			t.Errorf("%s: not refused", tc.name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, ". Hint: ") || !strings.Contains(msg, tc.hint) {
			t.Errorf("%s: want a hint naming %q, got %q", tc.name, tc.hint, msg)
		}
	}
}

// Names with no schema-inspection meaning get the plain refusal.
func TestCatalogRefusal_NoHint(t *testing.T) {
	for _, sql := range []string{
		"SELECT query FROM pg_stat_activity",
		"SELECT pg_sleep(1)",
		"SELECT * FROM information_schema.sql_features",
	} {
		err := ValidateNoCrossSchemaRefs(sql, "tenant_x")
		if err == nil {
			err = ValidateNoCatalogRefs(sql)
		}
		if err == nil {
			t.Fatalf("%q: not refused", sql)
		}
		if strings.Contains(err.Error(), "Hint:") {
			t.Errorf("%q: unexpected hint: %v", sql, err)
		}
	}
	// Another tenant's schema: the plain refusal, nothing more.
	err := ValidateNoCrossSchemaRefs("SELECT * FROM tenant_other.users", "tenant_x")
	if err == nil || strings.Contains(err.Error(), "Hint:") {
		t.Errorf("cross-tenant reference: got %v", err)
	}
}

func TestTenantMigration_CatalogHints(t *testing.T) {
	cases := []struct{ sql, want string }{
		{`CREATE TABLE t AS SELECT * FROM pg_policies;`, "/policies"},
		{`SELECT * FROM pg_catalog.pg_proc;`, "getDbFunction"},
		{`SELECT * FROM information_schema . columns;`, "describeTable"},
	}
	for _, tc := range cases {
		err := ValidateTenantMigrationSQL(tc.sql)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: want hint naming %q, got %v", tc.sql, tc.want, err)
		}
	}
	// The forbidden-schema message keeps its shape.
	err := ValidateTenantMigrationSQL(`CREATE TABLE public.x (id int);`)
	if err == nil || !strings.Contains(err.Error(), "forbidden schema (public....)") || strings.Contains(err.Error(), "Hint:") {
		t.Errorf("public reference: got %v", err)
	}
}
