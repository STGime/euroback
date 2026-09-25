package db

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WithSessionReset: session state left by any pool user (not only the
// query engine) is gone for the next user of the connection (#641).
func TestWithSessionReset(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" || testing.Short() {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	oneConn := func(c *pgxpool.Config) { c.MaxConns, c.MinConns = 1, 0 }
	pool, err := NewPool(ctx, url, WithSessionReset(), oneConn)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	defer pool.Close()
	for _, stmt := range []string{
		"SET default_transaction_read_only = on",
		"SELECT set_config('app.end_user_id', '00000000-0000-0000-0000-000000000003', false)",
		"CREATE TEMP TABLE reset_leak (x int)",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
		// One connection: the next acquire waits for the (async) reset
		// hook to hand the same connection back.
		var ro, uid string
		var noTemp bool
		if err := pool.QueryRow(ctx, `SELECT current_setting('default_transaction_read_only'),
			coalesce(current_setting('app.end_user_id', true), ''),
			to_regclass('pg_temp.reset_leak') IS NULL`).Scan(&ro, &uid, &noTemp); err != nil {
			t.Fatal(err)
		}
		if ro != "off" || uid != "" || !noTemp {
			t.Fatalf("after %q: read_only=%s end_user_id=%q temp_gone=%v", stmt, ro, uid, noTemp)
		}
	}
}
