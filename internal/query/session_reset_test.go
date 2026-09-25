package query

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
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
	// Production's shared pools use DescribeExec (internal/db); the RPC
	// test below covers pgx's default statement-caching mode.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
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

// RPC bodies (and triggers) are customer code on the shared pool: their
// session-level changes must not survive onto the next request.
func TestCallFunction_ResetsSessionState(t *testing.T) {
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

	fn := pgx.Identifier{schema, "leaky"}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE FUNCTION "+fn+`() RETURNS int LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM set_config('app.end_user_id', '00000000-0000-0000-0000-000000000002', false);
			CREATE TEMP TABLE IF NOT EXISTS rpc_leak (x int);
			RETURN 1;
		END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CallFunction(ctx, schema, "leaky", nil); err != nil {
		t.Fatalf("CallFunction: %v", err)
	}
	var uid string
	var noTemp bool
	if err := pool.QueryRow(ctx, `SELECT coalesce(current_setting('app.end_user_id', true), ''),
		to_regclass('pg_temp.rpc_leak') IS NULL`).Scan(&uid, &noTemp); err != nil {
		t.Fatal(err)
	}
	if uid != "" {
		t.Errorf("app.end_user_id leaked from an RPC body: %q", uid)
	}
	if !noTemp {
		t.Error("temp table leaked from an RPC body")
	}
}

// The server refuses a second statement on the customer-statement path,
// independent of the lexical checks.
func TestExecCustomerStatement_SingleStatement(t *testing.T) {
	pool, _, _ := setupTestDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := execCustomerStatement(ctx, tx, "SELECT 1; SELECT 2"); err == nil {
		t.Error("execCustomerStatement ran two statements, want an error")
	}
}

// String-literal parsing is pinned before every customer statement, even
// if earlier customer code in the same transaction changed it.
func TestExecuteSQLTransaction_PinsStringParsing(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	e := NewQueryEngine(pool)
	res, err := e.ExecuteSQLTransaction(context.Background(), schema, []string{
		"DO $$BEGIN PERFORM set_config('standard_conforming_strings', 'off', true); END$$",
		"SELECT current_setting('standard_conforming_strings') AS v",
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := res[len(res)-1].Rows[0]["v"]; got != "on" {
		t.Errorf("standard_conforming_strings = %v for the next customer statement, want on", got)
	}
}
