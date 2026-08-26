package query

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Tests for ApplyPolicyPreset — the SQL each preset emits into
// pg_policies. We test at the pg_policies-inspection level (rather
// than end-to-end enforcement) because RLS enforcement against
// eurobase_gateway on the shared pool has its own coverage in
// internal/dbprovider/rls_isolation_dedicated_test.go, and here we
// specifically want to lock down the POLICY DDL the presets emit —
// that's what regressed for the read_only preset and what a customer
// asked us to add for service_only (#TBD).

// policyRow is a projection of pg_policies for one policy on one table.
type policyRow struct {
	Name     string
	Cmd      string // one of ALL/SELECT/INSERT/UPDATE/DELETE (Postgres exposes 'r' etc; we normalise)
	Qual     string // USING clause text, "" if absent
	WithChk  string // WITH CHECK clause text, "" if absent
}

func loadPoliciesForTable(t *testing.T, pool *pgxpool.Pool, schema, table string) []policyRow {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx,
		`SELECT policyname, cmd, coalesce(qual, ''), coalesce(with_check, '')
		   FROM pg_policies
		  WHERE schemaname = $1 AND tablename = $2
		  ORDER BY policyname`,
		schema, table,
	)
	if err != nil {
		t.Fatalf("query pg_policies: %v", err)
	}
	defer rows.Close()
	var out []policyRow
	for rows.Next() {
		var p policyRow
		if err := rows.Scan(&p.Name, &p.Cmd, &p.Qual, &p.WithChk); err != nil {
			t.Fatalf("scan pg_policies row: %v", err)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// mkPresetTable creates a fresh test table with a user_id column and
// returns its name. Caller is responsible for dropping. Each preset
// test uses its own table so the drop-and-reapply logic in
// ApplyPolicyPreset doesn't collide across tests.
func mkPresetTable(t *testing.T, pool *pgxpool.Pool, schema, table string) {
	t.Helper()
	ctx := context.Background()
	cols := []ColumnDefinition{
		{Name: "id", Type: "uuid", Nullable: false, DefaultValue: "public.uuid_generate_v4()", IsPrimaryKey: true},
		{Name: "user_id", Type: "uuid", Nullable: true},
		{Name: "data", Type: "text", Nullable: true},
	}
	if err := CreateTable(ctx, pool, schema, table, cols); err != nil {
		t.Fatalf("CreateTable(%s): %v", table, err)
	}
	t.Cleanup(func() { _ = DropTable(context.Background(), pool, schema, table) })
}

// TestApplyPolicyPreset_ServiceOnly — the newly-added preset. One
// policy, FOR ALL, USING (public.is_service_role()) — nothing else.
// Anon (public key) is denied every op; secret key bypasses.
func TestApplyPolicyPreset_ServiceOnly(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	tbl := "service_only_t"
	mkPresetTable(t, pool, schema, tbl)

	if err := ApplyPolicyPreset(ctx, pool, schema, tbl, "service_only", "user_id"); err != nil {
		t.Fatalf("ApplyPolicyPreset(service_only): %v", err)
	}

	policies := loadPoliciesForTable(t, pool, schema, tbl)
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d: %+v", len(policies), policies)
	}
	p := policies[0]
	if p.Name != "service_only" {
		t.Errorf("policy name = %q, want service_only", p.Name)
	}
	// Postgres cmd column: 'ALL' when policy is FOR ALL.
	if p.Cmd != "ALL" {
		t.Errorf("policy cmd = %q, want ALL", p.Cmd)
	}
	if !strings.Contains(p.Qual, "is_service_role") {
		t.Errorf("USING clause must reference is_service_role; got %q", p.Qual)
	}
}

// TestApplyPolicyPreset_ReadOnly_ServiceCanWrite — regression coverage
// for the customer report: read_only previously only emitted the
// SELECT-USING-true policy, leaving INSERT/UPDATE/DELETE deny-all for
// everyone including the secret key. The fix adds a companion FOR ALL
// USING (is_service_role()) policy so service writes are permitted.
// The SELECT branch of that FOR ALL is a no-op because the FOR SELECT
// USING (true) policy already permits public reads (policies OR).
func TestApplyPolicyPreset_ReadOnly_ServiceCanWrite(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	tbl := "read_only_t"
	mkPresetTable(t, pool, schema, tbl)

	if err := ApplyPolicyPreset(ctx, pool, schema, tbl, "read_only", "user_id"); err != nil {
		t.Fatalf("ApplyPolicyPreset(read_only): %v", err)
	}

	policies := loadPoliciesForTable(t, pool, schema, tbl)
	if len(policies) != 2 {
		t.Fatalf("expected 2 policies (public_read + service_ops), got %d: %+v", len(policies), policies)
	}

	byName := map[string]policyRow{}
	for _, p := range policies {
		byName[p.Name] = p
	}

	pub, ok := byName["read_only_public_read"]
	if !ok {
		t.Fatalf("missing read_only_public_read policy; got %+v", policies)
	}
	if pub.Cmd != "SELECT" {
		t.Errorf("public read policy cmd = %q, want SELECT", pub.Cmd)
	}
	if !strings.Contains(pub.Qual, "true") {
		t.Errorf("public read USING must be 'true'; got %q", pub.Qual)
	}

	svc, ok := byName["read_only_service_ops"]
	if !ok {
		t.Fatalf("missing read_only_service_ops policy; got %+v", policies)
	}
	if svc.Cmd != "ALL" {
		t.Errorf("service policy cmd = %q, want ALL", svc.Cmd)
	}
	if !strings.Contains(svc.Qual, "is_service_role") {
		t.Errorf("service USING must reference is_service_role; got %q", svc.Qual)
	}
}

// TestApplyPolicyPreset_OwnerAccess_ServiceBypassPreserved — regression
// coverage: the existing owner_access preset must keep the
// `is_service_role() OR user_id = auth_uid()` disjunction on every
// operation. Any future refactor that drops the bypass on this preset
// would silently break server-side backends touching owned tables.
func TestApplyPolicyPreset_OwnerAccess_ServiceBypassPreserved(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	tbl := "owner_access_t"
	mkPresetTable(t, pool, schema, tbl)

	if err := ApplyPolicyPreset(ctx, pool, schema, tbl, "owner_access", "user_id"); err != nil {
		t.Fatalf("ApplyPolicyPreset(owner_access): %v", err)
	}

	policies := loadPoliciesForTable(t, pool, schema, tbl)
	if len(policies) != 4 {
		t.Fatalf("expected 4 policies (select/insert/update/delete), got %d: %+v", len(policies), policies)
	}

	for _, p := range policies {
		// Every clause that exists must include the is_service_role() branch.
		if p.Qual != "" && !strings.Contains(p.Qual, "is_service_role") {
			t.Errorf("owner_access policy %q USING must include is_service_role; got %q", p.Name, p.Qual)
		}
		if p.WithChk != "" && !strings.Contains(p.WithChk, "is_service_role") {
			t.Errorf("owner_access policy %q WITH CHECK must include is_service_role; got %q", p.Name, p.WithChk)
		}
	}
}

// TestApplyPolicyPreset_UnknownPreset_ReturnsError — a bogus preset
// name should fail loudly rather than silently no-op. The DDL handler
// bubbles this to a 400 (see ddl_handler.go post-fix).
func TestApplyPolicyPreset_UnknownPreset_ReturnsError(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	tbl := "unknown_preset_t"
	mkPresetTable(t, pool, schema, tbl)

	err := ApplyPolicyPreset(ctx, pool, schema, tbl, "definitely_not_a_preset", "user_id")
	if err == nil {
		t.Fatalf("expected error for unknown preset, got nil")
	}
	if !strings.Contains(err.Error(), "unknown policy preset") {
		t.Errorf("error message should mention 'unknown policy preset'; got %q", err.Error())
	}
}

// TestApplyPolicyPreset_MissingUserIDColumn_ReturnsError — when a
// preset that references the user-id column is applied to a table
// that doesn't have that column, the underlying CREATE POLICY DDL
// fails with a Postgres 42703 (column does not exist). Prior to the
// ddl_handler.go fix the caller got a silent 200 with the table left
// in an RLS-enabled-no-policies deny-all state — the exact class of
// bug the customer reported. This test asserts the error propagates
// up out of ApplyPolicyPreset; the handler-level rollback is covered
// by the change to ddl_handler.go itself (verified by the reviewer).
func TestApplyPolicyPreset_MissingUserIDColumn_ReturnsError(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	tbl := "no_user_id_t"

	// Table WITHOUT a user_id column.
	cols := []ColumnDefinition{
		{Name: "id", Type: "uuid", Nullable: false, DefaultValue: "public.uuid_generate_v4()", IsPrimaryKey: true},
		{Name: "data", Type: "text", Nullable: true},
	}
	if err := CreateTable(ctx, pool, schema, tbl, cols); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	t.Cleanup(func() { _ = DropTable(context.Background(), pool, schema, tbl) })

	err := ApplyPolicyPreset(ctx, pool, schema, tbl, "owner_access", "user_id")
	if err == nil {
		t.Fatalf("expected error when preset references non-existent column, got nil")
	}
	// Message should mention the failure — the exact Postgres wording
	// varies but the wrapping "apply policy" prefix from ddl.go is stable.
	if !strings.Contains(err.Error(), "apply policy") {
		t.Errorf("error should be wrapped with 'apply policy'; got %q", err.Error())
	}
}
