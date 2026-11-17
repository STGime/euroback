package query

import (
	"net/http"
	"net/http/httptest"
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
		{"constraints", "SELECT conname FROM pg_constraint", "describeTable"},
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
		// Not returned by the schema API, so no (wrong) pointer.
		"SELECT pg_get_constraintdef(1)",
		"SELECT * FROM information_schema.check_constraints",
		"SELECT * FROM pg_description",
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

// Edge forms of the migrations validator's qualified-schema check: still
// refused, hint only where it applies.
func TestTenantMigration_QualifiedSchemaEdgeForms(t *testing.T) {
	cases := []struct {
		sql      string
		wantHint bool
	}{
		{`SELECT * FROM PUBLIC . x;`, false},
		{`SELECT 1 FROM public.`, false},
		{`SELECT * FROM pg_catalog."pg_proc";`, false},
		{`SELECT * FROM tenant_abc.foo;`, false},
		{`SELECT * FROM INFORMATION_SCHEMA.Routines;`, true},
	}
	for _, tc := range cases {
		err := ValidateTenantMigrationSQL(tc.sql)
		if err == nil || !strings.Contains(err.Error(), "forbidden schema") {
			t.Errorf("%q: want forbidden-schema refusal, got %v", tc.sql, err)
			continue
		}
		if got := strings.Contains(err.Error(), "Hint:"); got != tc.wantHint {
			t.Errorf("%q: hint = %v, want %v (%v)", tc.sql, got, tc.wantHint, err)
		}
	}
}

// Every catalog hint key is a name the guard actually refuses.
func TestCatalogHints_KeysReachable(t *testing.T) {
	for k := range catalogHints {
		if !strings.HasPrefix(k, "pg_") || catalogAllowedNames[k] {
			t.Errorf("hint key %q can never be hit", k)
		}
	}
}

func TestWithoutHint(t *testing.T) {
	if got := withoutHint("refused. Hint: use x"); got != "refused" {
		t.Errorf("got %q", got)
	}
	if got := withoutHint("refused"); got != "refused" {
		t.Errorf("got %q", got)
	}
}

// The hint is for the developer: the platform SQL endpoint returns it, the
// SDK endpoint (app callers) doesn't.
func TestSQLHandlers_HintOnlyOnPlatformPath(t *testing.T) {
	engine := NewQueryEngine(nil) // the refusal happens before any DB work
	for name, tc := range map[string]struct {
		h        http.HandlerFunc
		wantHint bool
	}{
		"SDK /v1/db/sql":     {HandleSQL(engine), false},
		"platform /data/sql": {HandlePlatformSQL(engine), true},
	} {
		rr := httptest.NewRecorder()
		tc.h.ServeHTTP(rr, sqlReq("SELECT prosrc FROM pg_proc"))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", name, rr.Code)
		}
		if got := strings.Contains(rr.Body.String(), "Hint:"); got != tc.wantHint {
			t.Errorf("%s: hint in body = %v, want %v: %s", name, got, tc.wantHint, rr.Body.String())
		}
	}
}
