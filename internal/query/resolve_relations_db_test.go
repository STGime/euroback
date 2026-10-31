package query

import (
	"context"
	"fmt"
	"testing"
)

// The embed's foreign-key lookup reads pg_constraint, so it finds a key
// whatever role the catalog pool logs in as (information_schema showed a
// referenced table only to its owner). Single-column keys only.
func TestResolveRelations_DB(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	q := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, fmt.Sprintf(sql, quoteIdent(schema))); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	q(`CREATE TABLE %[1]s.rr_authors (id int PRIMARY KEY, name text)`)
	q(`CREATE TABLE %[1]s.rr_books (id int PRIMARY KEY, author int REFERENCES %[1]s.rr_authors(id), title text)`)
	q(`CREATE TABLE %[1]s.rr_pair (a int, b int, PRIMARY KEY (a, b))`)
	q(`CREATE TABLE %[1]s.rr_link (x int, y int, FOREIGN KEY (x, y) REFERENCES %[1]s.rr_pair(a, b))`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %[1]s.rr_link, %[1]s.rr_pair, %[1]s.rr_books, %[1]s.rr_authors`, quoteIdent(schema)))
	})

	e := NewQueryEngine(pool)
	got, err := e.resolveRelations(ctx, schema, "rr_books", []Relation{{Table: "rr_authors", Columns: []string{"name"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ForeignKey != "author" || got[0].ReferencedColumn != "id" {
		t.Fatalf("got %+v, want author → id", got)
	}

	if _, err := e.resolveRelations(ctx, schema, "rr_authors", []Relation{{Table: "rr_books"}}); err == nil {
		t.Fatal("reverse direction: want no foreign key found")
	}
	if _, err := e.resolveRelations(ctx, schema, "rr_link", []Relation{{Table: "rr_pair"}}); err == nil {
		t.Fatal("composite key: want no foreign key found")
	}
}
