package query

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The schema endpoint reads columns, constraints, indexes, triggers and RLS
// flags for the whole schema in one query each. This checks that it gives
// exactly what the per-table helpers give, on a schema with the cases they
// handle: primary and composite keys, foreign keys (also composite), a
// single-column UNIQUE, a multi-column UNIQUE (not marked), a plain and a
// unique index, a trigger, and RLS on one table.
func TestSchemaBatchMatchesPerTable_DB(t *testing.T) {
	// Needs only a plain Postgres (no platform migrations): a throwaway
	// schema is created and dropped.
	url := os.Getenv("SCHEMA_BATCH_TEST_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" || testing.Short() {
		t.Skip("set SCHEMA_BATCH_TEST_URL (or DATABASE_URL) to a Postgres to run this")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("cannot ping: %v", err)
	}
	schema := fmt.Sprintf("schema_batch_test_%d", os.Getpid())
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+quoteIdent(schema)+" CASCADE") })
	q := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, fmt.Sprintf(sql, quoteIdent(schema))); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	q(`CREATE TABLE %[1]s.sb_authors (id int PRIMARY KEY, email text UNIQUE, name text NOT NULL DEFAULT 'x')`)
	q(`CREATE TABLE %[1]s.sb_books (id int PRIMARY KEY, author int REFERENCES %[1]s.sb_authors(id), title text, UNIQUE (title, author))`)
	q(`CREATE TABLE %[1]s.sb_pair (a int, b int, PRIMARY KEY (a, b))`)
	q(`CREATE TABLE %[1]s.sb_link (x int, y int, note text, FOREIGN KEY (x, y) REFERENCES %[1]s.sb_pair(a, b))`)
	q(`CREATE INDEX sb_books_title_idx ON %[1]s.sb_books (title)`)
	q(`CREATE UNIQUE INDEX sb_link_note_idx ON %[1]s.sb_link (note)`)
	q(`CREATE FUNCTION %[1]s.sb_touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	q(`CREATE TRIGGER sb_books_touch BEFORE INSERT OR UPDATE ON %[1]s.sb_books FOR EACH ROW EXECUTE FUNCTION %[1]s.sb_touch()`)
	q(`ALTER TABLE %[1]s.sb_books ENABLE ROW LEVEL SECURITY`)

	tables, err := GetSchemaTables(ctx, pool, schema)
	if err != nil {
		t.Fatal(err)
	}
	colsBy, err := schemaColumns(ctx, pool, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	consBy, err := schemaConstraints(ctx, pool, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	idxBy, err := schemaIndexes(ctx, pool, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	trgBy, err := schemaTriggers(ctx, pool, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	rlsBy, err := schemaRLS(ctx, pool, schema)
	if err != nil {
		t.Fatal(err)
	}

	asJSON := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	seen := 0
	for _, tbl := range tables {
		if len(tbl) < 3 || tbl[:3] != "sb_" {
			continue
		}
		seen++
		cols, err := GetTableColumns(ctx, pool, schema, tbl)
		if err != nil {
			t.Fatal(err)
		}
		cols, err = GetTableConstraints(ctx, pool, schema, tbl, cols)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := asJSON(applyConstraints(colsBy[tbl], consBy[tbl])), asJSON(cols); got != want {
			t.Errorf("%s columns:\n batch     %s\n per-table %s", tbl, got, want)
		}
		idx, err := GetTableIndexes(ctx, pool, schema, tbl)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := asJSON(idxBy[tbl]), asJSON(idx); got != want {
			t.Errorf("%s indexes:\n batch     %s\n per-table %s", tbl, got, want)
		}
		trg, err := GetTableTriggers(ctx, pool, schema, tbl)
		if err != nil {
			t.Fatal(err)
		}
		batchTrg := trgBy[tbl]
		if batchTrg == nil {
			batchTrg = []DBTrigger{}
		}
		if got, want := asJSON(batchTrg), asJSON(trg); got != want {
			t.Errorf("%s triggers:\n batch     %s\n per-table %s", tbl, got, want)
		}
		var rls bool
		if err := pool.QueryRow(ctx,
			`SELECT relrowsecurity FROM pg_class WHERE relname = $1 AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = $2)`,
			tbl, schema).Scan(&rls); err != nil {
			t.Fatal(err)
		}
		if rlsBy[tbl] != rls {
			t.Errorf("%s rls: batch %v, per-table %v", tbl, rlsBy[tbl], rls)
		}
	}
	if seen != 4 {
		t.Fatalf("expected the 4 test tables, saw %d", seen)
	}

	// Spot-check the content, so a shared bug can't hide behind equality.
	books := applyConstraints(colsBy["sb_books"], consBy["sb_books"])
	var authorFK, titleUnique bool
	for _, c := range books {
		if c.Name == "author" && c.ForeignKey != nil && c.ForeignKey.ReferencedTable == "sb_authors" {
			authorFK = true
		}
		if c.Name == "title" && c.IsUnique {
			titleUnique = true
		}
	}
	if !authorFK || titleUnique {
		t.Errorf("sb_books: author FK %v (want true), title unique %v (want false: only a multi-column UNIQUE)", authorFK, titleUnique)
	}
	if len(trgBy["sb_books"]) != 1 || !rlsBy["sb_books"] || rlsBy["sb_authors"] {
		t.Errorf("sb_books trigger/RLS wrong: triggers %d, rls books %v authors %v", len(trgBy["sb_books"]), rlsBy["sb_books"], rlsBy["sb_authors"])
	}
	if len(idxBy["sb_books"]) != 1 || len(idxBy["sb_link"]) != 1 || !idxBy["sb_link"][0].IsUnique {
		t.Errorf("indexes wrong: %+v / %+v", idxBy["sb_books"], idxBy["sb_link"])
	}
}
