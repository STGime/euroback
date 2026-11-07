package query

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Errors raised by a developer's own trigger or function reach REST
// callers with their message and code, not as a 500.
func TestHandleQueryError_TriggerErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
		wantCode   string
	}{
		{"read-only freeze is 409", &pgconn.PgError{Code: "25006", Message: "table legacy is read-only during migration"},
			http.StatusConflict, "table legacy is read-only during migration", "25006"},
		{"read-only without message", &pgconn.PgError{Code: "25006"},
			http.StatusConflict, "table is read-only", "25006"},
		{"raise exception is 400", fmt.Errorf("insert row: %w", &pgconn.PgError{Code: "P0001", Message: "balance cannot be negative", Detail: "secret"}),
			http.StatusBadRequest, "balance cannot be negative", "P0001"},
		{"assert failure is 400", &pgconn.PgError{Code: "P0004", Message: "assertion failed"},
			http.StatusBadRequest, "assertion failed", "P0004"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			if !handleQueryError(rr, tc.err) {
				t.Fatal("expected handleQueryError to handle the error")
			}
			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			var body map[string]string
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["error"] != tc.wantMsg || body["code"] != tc.wantCode {
				t.Errorf("body = %v, want error %q code %q", body, tc.wantMsg, tc.wantCode)
			}
			if _, ok := body["detail"]; ok {
				t.Errorf("body leaks detail: %v", body)
			}
		})
	}

	// Other errors keep their old handling (here: unhandled → caller's 500).
	if handleQueryError(httptest.NewRecorder(), &pgconn.PgError{Code: "XX000", Message: "internal"}) {
		t.Error("XX000 should not be handled here")
	}
}

// A table frozen with a statement-level trigger refuses REST writes with
// 409 through the real engine, and reads still work.
func TestTriggerFreeze_RealDB(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)
	q := quoteIdent(schema)

	for _, stmt := range []string{
		fmt.Sprintf(`CREATE TABLE %s.frozen_things (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text)`, q),
		fmt.Sprintf(`INSERT INTO %s.frozen_things (name) VALUES ('before')`, q),
		fmt.Sprintf(`CREATE FUNCTION %s.migration_freeze() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'table %% is read-only during migration', TG_TABLE_NAME USING ERRCODE = '25006'; END $$`, q),
		fmt.Sprintf(`CREATE TRIGGER migration_freeze BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON %s.frozen_things
			FOR EACH STATEMENT EXECUTE FUNCTION %s.migration_freeze()`, q, q),
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	var id string
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT id::text FROM %s.frozen_things`, q)).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}

	check := func(op string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected the freeze to refuse the write", op)
		}
		rr := httptest.NewRecorder()
		if !handleQueryError(rr, err) || rr.Code != http.StatusConflict {
			t.Errorf("%s: status %d (handled=%v), want 409; err = %v", op, rr.Code, rr.Code != 200, err)
		}
		var body map[string]string
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		if body["error"] != "table frozen_things is read-only during migration" || body["code"] != "25006" {
			t.Errorf("%s: body = %v", op, body)
		}
	}
	_, err := engine.InsertRow(ctx, schema, "frozen_things", map[string]interface{}{"name": "x"})
	check("insert", err)
	_, err = engine.UpdateRow(ctx, schema, "frozen_things", id, map[string]interface{}{"name": "y"})
	check("update", err)
	check("delete", engine.DeleteRow(ctx, schema, "frozen_things", id))

	rows, _, err := engine.SelectRows(ctx, schema, "frozen_things", QueryParams{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Errorf("read during freeze: rows %d, err %v", len(rows), err)
	}
}
