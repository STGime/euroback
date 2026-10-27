package query

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestGetTableConstraints_DB reads keys as a role that doesn't own the
// tables, like the runtime pool behind GET …/schema: foreign keys must
// come back with their target (information_schema hid it from
// non-owners), composite keys column by column.
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run TestGetTableConstraints_DB
func TestGetTableConstraints_DB(t *testing.T) {
	adminURL := os.Getenv("CONVERGE_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("CONVERGE_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	schema := fmt.Sprintf("tenant_fkmeta_%d", os.Getpid())
	ident := pgx.Identifier{schema}.Sanitize()
	owner := pgx.Identifier{schema + "_owner"}.Sanitize()
	app := pgx.Identifier{schema + "_app"}.Sanitize()
	cleanup := func() {
		for _, q := range []string{"DROP SCHEMA IF EXISTS " + ident + " CASCADE", "DROP ROLE IF EXISTS " + owner, "DROP ROLE IF EXISTS " + app} {
			if _, err := admin.Exec(context.Background(), q); err != nil {
				t.Logf("cleanup %s: %v", q, err)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE ROLE " + owner + " NOLOGIN",
		"CREATE ROLE " + app + " NOLOGIN",
		"CREATE SCHEMA " + ident + " AUTHORIZATION " + owner,
		"SET ROLE " + owner,
		"CREATE TABLE " + ident + ".teams (id uuid PRIMARY KEY, season int NOT NULL, code text UNIQUE, UNIQUE (id, season))",
		"CREATE TABLE " + ident + ".players (id uuid PRIMARY KEY, team_id uuid NOT NULL, season int NOT NULL, coach_id uuid," +
			" CONSTRAINT players_team_fk FOREIGN KEY (team_id, season) REFERENCES " + ident + ".teams (id, season)," +
			" CONSTRAINT players_coach_fk FOREIGN KEY (coach_id) REFERENCES " + ident + ".teams (id))",
		"GRANT USAGE ON SCHEMA " + ident + " TO " + app,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + ident + " TO " + app,
		"RESET ROLE",
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+app)
		return err
	}
	asApp, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(asApp.Close)

	cols, err := GetTableColumns(ctx, asApp, schema, "players")
	if err != nil {
		t.Fatal(err)
	}
	cols, err = GetTableConstraints(ctx, asApp, schema, "players", cols)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ColumnInfo{}
	for _, c := range cols {
		got[c.Name] = c
	}
	wantFK := map[string]ForeignKeyInfo{
		"team_id":  {ConstraintName: "players_team_fk", ReferencedTable: "teams", ReferencedColumn: "id"},
		"season":   {ConstraintName: "players_team_fk", ReferencedTable: "teams", ReferencedColumn: "season"},
		"coach_id": {ConstraintName: "players_coach_fk", ReferencedTable: "teams", ReferencedColumn: "id"},
	}
	for name, want := range wantFK {
		fk := got[name].ForeignKey
		if fk == nil || *fk != want {
			t.Errorf("%s foreign key = %+v, want %+v", name, fk, want)
		}
	}
	if !got["id"].IsPrimaryKey || got["id"].ForeignKey != nil {
		t.Errorf("players.id = %+v, want primary key without foreign key", got["id"])
	}

	cols, err = GetTableColumns(ctx, asApp, schema, "teams")
	if err != nil {
		t.Fatal(err)
	}
	cols, err = GetTableConstraints(ctx, asApp, schema, "teams", cols)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		switch c.Name {
		case "id":
			if !c.IsPrimaryKey {
				t.Errorf("teams.id not marked primary key")
			}
		case "code":
			if !c.IsUnique {
				t.Errorf("teams.code not marked unique")
			}
		}
		if c.ForeignKey != nil {
			t.Errorf("teams.%s has foreign key %+v", c.Name, c.ForeignKey)
		}
	}
}
