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

// The platform SQL path (POST /platform/.../data/sql and .../sql/transaction)
// runs as eurobase_migrator, which has EXECUTE on RLS helper functions
// (public.is_service_role() etc.) and on public.uuid_generate_v4(). The
// console docs and Table Editor actively generate references qualified as
// `public.<helper>` — the strict variant would reject those. The
// PlatformSQLPublicAllowlist exempts specific well-known names while
// keeping every other public.* reference blocked, so cross-tenant table
// reads (public.subscriptions etc.) stay closed.
func TestValidateNoCrossSchemaRefsOpts_PlatformAllowlistAcceptsDocumentedHelpers(t *testing.T) {
	allowed := "tenant_abc"
	opts := CrossSchemaOptions{AllowedPublicNames: PlatformSQLPublicAllowlist}
	cases := []struct {
		name string
		sql  string
	}{
		{"is_service_role in RLS policy",
			"CREATE POLICY p ON t USING (public.is_service_role() OR user_id = public.current_end_user_id())"},
		{"is_internal_auth_path",
			"CREATE POLICY p ON t USING (public.is_internal_auth_path())"},
		{"uuid_generate_v4 as column default",
			"ALTER TABLE t ALTER COLUMN id SET DEFAULT public.uuid_generate_v4()"},
		{"gen_random_uuid qualified",
			"INSERT INTO t (id) VALUES (public.gen_random_uuid())"},
		{"mixed case helper name",
			"SELECT public.Is_Service_Role()"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateNoCrossSchemaRefsOpts(tc.sql, allowed, opts); err != nil {
				t.Errorf("ValidateNoCrossSchemaRefsOpts(%q) = %v, want nil (documented helper should be allowed)", tc.sql, err)
			}
		})
	}
}

// The exemption is name-based and precisely scoped: any public.<name>
// not in the allowlist must still be rejected, even when the allowlist
// is populated. Guards against a future exemption-list bug that would
// silently allow cross-tenant table reads.
func TestValidateNoCrossSchemaRefsOpts_PlatformAllowlistStillBlocksTables(t *testing.T) {
	allowed := "tenant_abc"
	opts := CrossSchemaOptions{AllowedPublicNames: PlatformSQLPublicAllowlist}
	cases := []struct {
		name string
		sql  string
	}{
		{"public.subscriptions blocked", "SELECT * FROM public.subscriptions"},
		{"public.invoices blocked", "UPDATE public.invoices SET status = 'paid'"},
		{"public.platform_users blocked", "SELECT email FROM public.platform_users"},
		{"public.projects blocked", "SELECT * FROM public.projects"},
		{"public.webhooks blocked", "SELECT * FROM public.webhooks"},
		{"other tenant still blocked", "SELECT * FROM tenant_other.users"},
		{"pg_catalog still blocked", "SELECT * FROM pg_catalog.pg_class"},
		{"function name that clashes with a table stays blocked if not exempted",
			"SELECT * FROM public.random_helper()"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNoCrossSchemaRefsOpts(tc.sql, allowed, opts)
			if err == nil {
				t.Fatalf("ValidateNoCrossSchemaRefsOpts(%q) = nil, want error (should reject non-exempt public reference)", tc.sql)
			}
		})
	}
}

// SDKPublicAllowlist is strictly smaller than PlatformSQLPublicAllowlist:
// the SDK path can call the id-default helpers qualified (needed once
// the SDK path's search_path drops the `, public` fallback), but MUST
// NOT be able to reference the RLS helpers — the SDK is a data-plane
// endpoint, not a policy-authoring one, and any `public.<helper>`
// reference in an SDK query is either a bug or an exfiltration attempt.
func TestValidateNoCrossSchemaRefsOpts_SDKAllowlistAcceptsIdDefaultsOnly(t *testing.T) {
	allowed := "tenant_abc"
	opts := CrossSchemaOptions{AllowedPublicNames: SDKPublicAllowlist}

	// Accepted — id-default helpers only.
	for _, sql := range []string{
		"INSERT INTO t (id) VALUES (public.uuid_generate_v4())",
		"INSERT INTO t (id) VALUES (public.gen_random_uuid())",
	} {
		if err := ValidateNoCrossSchemaRefsOpts(sql, allowed, opts); err != nil {
			t.Errorf("SDK path should allow %q, got %v", sql, err)
		}
	}

	// Rejected — RLS helpers not on the SDK list even though they
	// are on the platform list. Also rejected: every other
	// cross-tenant table read that was blocked before.
	for _, sql := range []string{
		"SELECT public.is_service_role()",
		"SELECT public.current_end_user_id()",
		"SELECT public.is_internal_auth_path()",
		"SELECT * FROM public.subscriptions",
		"SELECT * FROM public.platform_users",
		"SELECT * FROM tenant_other.users",
	} {
		if err := ValidateNoCrossSchemaRefsOpts(sql, allowed, opts); err == nil {
			t.Errorf("SDK path should reject %q, got nil", sql)
		}
	}
}

// Nil / empty AllowedPublicNames must behave exactly like the strict
// zero-arg ValidateNoCrossSchemaRefs — no exemptions, everything under
// public rejected. Prevents a future refactor from silently opening
// the SDK path (which passes zero opts).
func TestValidateNoCrossSchemaRefsOpts_NilOptsMatchesStrict(t *testing.T) {
	sql := "CREATE POLICY p ON t USING (public.is_service_role())"
	if err := ValidateNoCrossSchemaRefs(sql, "tenant_abc"); err == nil {
		t.Fatalf("strict variant should reject public.is_service_role() — no exemptions on the SDK path")
	}
	if err := ValidateNoCrossSchemaRefsOpts(sql, "tenant_abc", CrossSchemaOptions{}); err == nil {
		t.Fatalf("Opts variant with nil allowlist should behave identically to strict")
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
