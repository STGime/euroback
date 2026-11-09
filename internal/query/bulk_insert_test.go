package query

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func createBulkItems(t *testing.T, ctx context.Context, e *QueryEngine, schema string) {
	t.Helper()
	_, err := e.pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s.bulk_items (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		name text NOT NULL,
		qty int NOT NULL DEFAULT 7,
		note text
	)`, quoteIdent(schema)))
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func TestInsertRows(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)
	createBulkItems(t, ctx, engine, schema)

	rows, err := engine.InsertRows(ctx, schema, "bulk_items", []map[string]interface{}{
		{"name": "a", "qty": 1},
		{"name": "b"},
		{"name": "c", "note": "x"},
	})
	if err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for i, want := range []string{"a", "b", "c"} {
		if rows[i]["name"] != want {
			t.Errorf("row %d name = %v, want %s (input order)", i, rows[i]["name"], want)
		}
	}
	// A column a row doesn't name gets its default, not NULL.
	if fmt.Sprint(rows[1]["qty"]) != "7" {
		t.Errorf("row b qty = %v, want default 7", rows[1]["qty"])
	}
	if rows[0]["note"] != nil {
		t.Errorf("row a note = %v, want NULL", rows[0]["note"])
	}
}

func TestInsertRows_AllOrNothing(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)
	createBulkItems(t, ctx, engine, schema)

	// The second row violates NOT NULL on name: nothing is inserted.
	_, err := engine.InsertRows(ctx, schema, "bulk_items", []map[string]interface{}{
		{"name": "ok"},
		{"qty": 2},
	})
	if err == nil {
		t.Fatal("expected an error for the NOT NULL violation")
	}
	var n int
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s.bulk_items`, quoteIdent(schema))).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows inserted, want 0", n)
	}
}

func TestInsertRows_Refusals(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)
	createBulkItems(t, ctx, engine, schema)

	if _, err := engine.InsertRows(ctx, schema, "bulk_items", nil); !errors.Is(err, ErrBulkInsertSize) {
		t.Errorf("empty: err = %v, want ErrBulkInsertSize", err)
	}
	tooMany := make([]map[string]interface{}, MaxBulkInsertRows+1)
	for i := range tooMany {
		tooMany[i] = map[string]interface{}{"name": "x"}
	}
	if _, err := engine.InsertRows(ctx, schema, "bulk_items", tooMany); !errors.Is(err, ErrBulkInsertSize) {
		t.Errorf("too many: err = %v, want ErrBulkInsertSize", err)
	}
	if _, err := engine.InsertRows(ctx, schema, "bulk_items", []map[string]interface{}{{"name": "a", "nope": 1}}); err == nil || !isValidationError(err) {
		t.Errorf("unknown column: err = %v, want a validation error", err)
	}
	// Credential tables stay refused, as for single inserts.
	if _, err := engine.InsertRows(ctx, schema, "refresh_tokens", []map[string]interface{}{{"token_hash": "x"}}); err == nil {
		t.Error("refresh_tokens: expected a refusal")
	}
}

func TestInsertRows_AllDefaults(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s.bulk_defaults (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), n int DEFAULT 1)`, quoteIdent(schema))); err != nil {
		t.Fatal(err)
	}
	rows, err := engine.InsertRows(ctx, schema, "bulk_defaults", []map[string]interface{}{{}, {}})
	if err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}

func TestInsertRows_TriggerSkipsRow(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)
	createBulkItems(t, ctx, engine, schema)
	qs := quoteIdent(schema)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %[1]s.skip_dupes() RETURNS trigger LANGUAGE plpgsql AS $f$
		BEGIN IF NEW.name = 'skip' THEN RETURN NULL; END IF; RETURN NEW; END $f$;
		CREATE TRIGGER skip_dupes BEFORE INSERT ON %[1]s.bulk_items
		FOR EACH ROW EXECUTE FUNCTION %[1]s.skip_dupes();`, qs)); err != nil {
		t.Fatal(err)
	}
	_, err := engine.InsertRows(ctx, schema, "bulk_items", []map[string]interface{}{{"name": "a"}, {"name": "skip"}})
	if !errors.Is(err, ErrBulkInsertSkipped) {
		t.Fatalf("err = %v, want ErrBulkInsertSkipped", err)
	}
	var n int
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s.bulk_items`, qs)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows inserted, want 0 (rolled back)", n)
	}
}
