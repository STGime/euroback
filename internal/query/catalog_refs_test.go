package query

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Regression coverage for the cross-tenant pg_stat_activity disclosure
// (security report 2026-09-16). The cross-schema scanner is dot-keyed,
// so a bare `pg_stat_activity` — which resolves via the always-implicit
// pg_catalog search path — was never examined, and every SDK tenant
// (shared eurobase_gateway role) could read every other tenant's
// in-flight SQL. ValidateNoCatalogRefs closes the whole `pg_`-prefixed
// class, bare or qualified, view or function.

func TestValidateNoCatalogRefs_RejectsCatalogSurface(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"bare pg_stat_activity (the reported vector)", "SELECT pid, usename, query FROM pg_stat_activity"},
		{"filtered marker sniff", "SELECT query FROM pg_stat_activity WHERE query LIKE '%XMARK%'"},
		{"qualified pg_catalog.pg_stat_activity", "SELECT * FROM pg_catalog.pg_stat_activity"},
		{"underlying stats function", "SELECT * FROM pg_stat_get_activity(NULL)"},
		{"pg_locks", "SELECT * FROM pg_locks"},
		{"pg_prepared_statements", "SELECT * FROM pg_prepared_statements"},
		{"pg_settings", "SELECT name, setting FROM pg_settings"},
		{"pg_roles", "SELECT rolname FROM pg_roles"},
		{"pg_shadow", "SELECT * FROM pg_shadow"},
		{"pg_stat_ssl", "SELECT * FROM pg_stat_ssl"},
		{"double-quoted identifier", `SELECT * FROM "pg_stat_activity"`},
		{"case-insensitive", "select * from PG_STAT_ACTIVITY"},
		{"subquery wrap", "SELECT q FROM (SELECT query AS q FROM pg_stat_activity) s"},
		{"cte wrap", "WITH a AS (SELECT * FROM pg_stat_activity) SELECT to_jsonb(a) FROM a"},
		{"join with tenant table", "SELECT t.id, p.query FROM todos t, pg_stat_activity p"},
		{"pg_sleep is not allowlisted", "SELECT pg_sleep(30)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateNoCatalogRefs(tc.sql); err == nil {
				t.Errorf("ValidateNoCatalogRefs(%q) = nil, want rejection", tc.sql)
			}
		})
	}
}

func TestValidateNoCatalogRefs_AllowsLegitimateSQL(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"plain tenant query", "SELECT id, title FROM todos WHERE completed = false"},
		{"join + aggregate", "SELECT u.email, count(*) FROM users u JOIN todos t ON t.user_id = u.id GROUP BY u.email"},
		{"string literal naming the view (not an identifier)", "SELECT 'pg_stat_activity' AS label FROM todos"},
		{"line comment naming the view", "-- pg_stat_activity\nSELECT 1 FROM todos"},
		{"block comment naming the view", "/* pg_stat_activity */ SELECT 1 FROM todos"},
		{"dollar-quoted body naming the view", "SELECT $$pg_stat_activity$$ FROM todos"},
		{"pg_temp stays allowed (matches cross-schema scanner)", "SELECT * FROM pg_temp.tmp_data"},
		{"allowlisted pg_typeof", "SELECT pg_typeof(id) FROM todos"},
		{"allowlisted pg_get_serial_sequence", "SELECT pg_get_serial_sequence('todos','id')"},
		{"allowlisted pg_backend_pid", "SELECT pg_backend_pid()"},
		{"column merely containing pg (no prefix)", "SELECT epg_value, mypg_col FROM metrics"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateNoCatalogRefs(tc.sql); err != nil {
				t.Errorf("ValidateNoCatalogRefs(%q) = %v, want nil", tc.sql, err)
			}
		})
	}
}

// The existing cross-schema scanner should still reject the dotted
// information_schema form (it's in schemaIsForbidden); pinned here so
// the two guards are known to overlap rather than assumed to.
func TestCrossSchema_DottedInformationSchemaStillRejected(t *testing.T) {
	if err := ValidateNoCrossSchemaRefs("SELECT schema_name FROM information_schema.schemata", "tenant_x"); err == nil {
		t.Error("dotted information_schema reference should be rejected by the cross-schema scanner")
	}
}

// sqlReq builds a /sql request carrying the tenant schema in context —
// the same shape handleSQLInternal reads. No DB: every rejection under
// test happens in the validators before ExecuteSQLWithOpts is reached,
// so a nil-pool engine is safe (it would only be touched on success).
func sqlReq(sql string) *http.Request {
	body, _ := json.Marshal(SQLRequest{SQL: sql})
	req := httptest.NewRequest(http.MethodPost, "/v1/db/sql", bytes.NewReader(body))
	return req.WithContext(ContextWithSchema(context.Background(), "tenant_probe"))
}

// TestSQLHandlers_RejectPgStatActivity_NoDB drives the actual HTTP
// handlers for BOTH customer-facing paths and asserts the reported
// query is refused with 400 before any database work.
func TestSQLHandlers_RejectPgStatActivity_NoDB(t *testing.T) {
	engine := NewQueryEngine(nil) // never reached on the rejection path
	handlers := map[string]http.HandlerFunc{
		"SDK /v1/db/sql":     HandleSQL(engine),
		"platform /data/sql": HandlePlatformSQL(engine),
	}
	vectors := []string{
		"SELECT pid, usename, client_addr, query FROM pg_stat_activity",
		"SELECT * FROM pg_stat_get_activity(NULL)",
		"SELECT * FROM pg_catalog.pg_stat_activity",
	}
	for name, h := range handlers {
		for _, v := range vectors {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, sqlReq(v))
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: %q → want 400, got %d (body: %s)", name, v, rr.Code, rr.Body.String())
			}
			// Defense in depth: the bare / function forms are caught by
			// ValidateNoCatalogRefs, while the dotted pg_catalog form is
			// caught earlier by the cross-schema scanner. Either guard
			// refusing it is the intended outcome — assert the rejection
			// is one of the two, not which one.
			// Match the bare token: in the JSON body the quotes around
			// pg_catalog are escaped (`\"pg_catalog\"`), so matching the
			// quoted form would miss it.
			body := rr.Body.String()
			if !strings.Contains(body, "system catalog") && !strings.Contains(body, "pg_catalog") {
				t.Errorf("%s: %q → rejection should come from the catalog or cross-schema guard, got: %s", name, v, body)
			}
		}
	}

	// Transaction endpoint: the guard must apply per statement too.
	body, _ := json.Marshal(SQLTransactionRequest{Statements: []string{
		"SELECT 1",
		"SELECT query FROM pg_stat_activity",
	}})
	req := httptest.NewRequest(http.MethodPost, "/platform/x/data/sql/transaction", bytes.NewReader(body))
	req = req.WithContext(ContextWithSchema(context.Background(), "tenant_probe"))
	rr := httptest.NewRecorder()
	HandlePlatformSQLTransaction(engine).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "statement 2") {
		t.Errorf("transaction: want 400 naming statement 2, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestSDKSQL_PgStatActivity_EndToEnd runs against a real database
// (skips without DATABASE_URL, like the other engine tests). It first
// shows the disclosure exists at the DB layer for this pool's role —
// a concurrent session's marker query is readable through
// pg_stat_activity — and then that the SDK handler now refuses the
// same read, while a legitimate tenant query still succeeds (no
// over-blocking).
func TestSDKSQL_PgStatActivity_EndToEnd(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()

	// 1. Underlying exposure: another session of this role holding a
	// marker statement is visible via pg_stat_activity.
	const marker = "CROSSTENANT_XMARK_LIT=secret-token-123"
	go func() {
		_, _ = pool.Exec(ctx, "SELECT pg_sleep(4), '"+marker+"' AS tag")
	}()
	var seen string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := pool.QueryRow(ctx,
			`SELECT query FROM pg_stat_activity
			  WHERE query LIKE '%CROSSTENANT_XMARK%' AND pid <> pg_backend_pid() LIMIT 1`).Scan(&seen)
		if err == nil && seen != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(seen, marker) {
		t.Fatalf("expected to observe the concurrent marker query via pg_stat_activity (documents the exposure); got %q", seen)
	}

	// 2. The SDK handler refuses the same read.
	engine := NewQueryEngine(pool)
	h := HandleSQL(engine)
	mk := func(sql string) *http.Request {
		body, _ := json.Marshal(SQLRequest{SQL: sql})
		req := httptest.NewRequest(http.MethodPost, "/v1/db/sql", bytes.NewReader(body))
		return req.WithContext(ContextWithKeyType(ContextWithSchema(ctx, schema), "public"))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, mk("SELECT query FROM pg_stat_activity WHERE query LIKE '%CROSSTENANT_XMARK%'"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("SDK pg_stat_activity read: want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, mk("SELECT * FROM pg_stat_get_activity(NULL)"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("SDK pg_stat_get_activity read: want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	// 3. A legitimate tenant query still works through the same handler.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, mk("SELECT id, title FROM todos"))
	if rr.Code != http.StatusOK {
		t.Fatalf("legit tenant query: want 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}
