package enduser

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The GDPR export finds a user's rows through the owner column of each
// developer table, read from the catalog: the first of ownerColumns a
// table has, platform tables and non-tables skipped. Needs a PG16:
// ENDUSER_PASSKEY_TEST_URL.
func TestDiscoverUserOwnedTables_DB(t *testing.T) {
	url := os.Getenv("ENDUSER_PASSKEY_TEST_URL")
	if url == "" {
		t.Skip("ENDUSER_PASSKEY_TEST_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	schema := "gdpr_disc_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	for _, stmt := range []string{
		`CREATE SCHEMA ` + schema,
		`CREATE TABLE ` + schema + `.orders (id int, user_id uuid)`,
		`CREATE TABLE ` + schema + `.notes (id int, created_by uuid, owner_id uuid)`,
		`CREATE TABLE ` + schema + `.logs (id int, created_by uuid)`,
		`CREATE TABLE ` + schema + `.settings (id int)`,
		`CREATE TABLE ` + schema + `.users (id uuid, user_id uuid)`,
		`CREATE VIEW ` + schema + `.my_orders AS SELECT * FROM ` + schema + `.orders`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	got, err := discoverUserOwnedTables(ctx, pool, schema)
	if err != nil {
		t.Fatal(err)
	}
	want := []ownedTable{
		{table: "logs", column: "created_by"},
		{table: "notes", column: "owner_id"},
		{table: "orders", column: "user_id"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
