package query

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Session state set by customer SQL must not survive onto the next
// request that reuses the pooled connection (#641). A 1-connection pool
// guarantees reuse.
func TestExecuteSQL_ResetsSessionState(t *testing.T) {
	base, schema, _ := setupTestDB(t)
	ctx := context.Background()

	cfg := base.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	e := NewQueryEngine(pool)
	platform := ExecOptions{}

	// A committed (non-SELECT) statement that changes session state.
	for _, stmt := range []string{
		"SET default_transaction_read_only = on",
		"SET app.end_user_id = '00000000-0000-0000-0000-000000000001'",
		"CREATE TEMP TABLE leak (x int)",
	} {
		if _, _, err := e.ExecuteSQLWithOpts(ctx, schema, stmt, 10, platform); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// The same, via the transaction endpoint.
	if _, err := e.ExecuteSQLTransaction(ctx, schema, []string{"SET work_mem = '64MB'"}, 10); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	_, rows, err := e.ExecuteSQLWithOpts(ctx, schema, `SELECT
		current_setting('default_transaction_read_only') AS ro,
		coalesce(current_setting('app.end_user_id', true), '') AS uid,
		to_regclass('pg_temp.leak') IS NULL AS no_temp,
		current_setting('work_mem') AS wm`, 10, platform)
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	if r["ro"] != "off" {
		t.Errorf("default_transaction_read_only leaked: %v", r["ro"])
	}
	if r["uid"] != "" {
		t.Errorf("app.end_user_id leaked: %v", r["uid"])
	}
	if r["no_temp"] != true {
		t.Error("temp table leaked onto the next request")
	}
	if r["wm"] == "64MB" {
		t.Error("work_mem from the transaction endpoint leaked")
	}
}
