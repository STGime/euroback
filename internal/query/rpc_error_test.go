package query

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRPCErrorFor(t *testing.T) {
	wrap := func(pg *pgconn.PgError) error { return fmt.Errorf("execute function: %w", pg) }

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
		wantCode   string
	}{
		{
			name:       "RAISE EXCEPTION keeps the function's own message, not its detail",
			err:        wrap(&pgconn.PgError{Code: "P0001", Message: "only a parent can read this mission", Detail: "secret-value"}),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "only a parent can read this mission",
			wantCode:   "P0001",
		},
		{
			name:       "permission denied (RLS / grants) is 403",
			err:        wrap(&pgconn.PgError{Code: "42501", Message: "new row violates row-level security policy for table \"answers\""}),
			wantStatus: http.StatusForbidden,
			wantMsg:    "permission denied",
			wantCode:   "42501",
		},
		{
			name:       "wrong argument names are 400 with Postgres' message",
			err:        wrap(&pgconn.PgError{Code: "42883", Message: "function f(x => integer) does not exist", Hint: "No function matches"}),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "function f(x => integer) does not exist",
			wantCode:   "42883",
		},
		{
			name:       "unique violation hides the offending value",
			err:        wrap(&pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint", Detail: "Key (email)=(kid@example.com) already exists."}),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "value violates a unique constraint",
			wantCode:   "23505",
		},
		{
			name:       "timeout is 503",
			err:        wrap(&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}),
			wantStatus: http.StatusServiceUnavailable,
			wantMsg:    "query timed out",
			wantCode:   "57014",
		},
		{
			name:       "function ended without RETURN is 400",
			err:        wrap(&pgconn.PgError{Code: "2F005", Message: "control reached end of function without RETURN"}),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "control reached end of function without RETURN",
			wantCode:   "2F005",
		},
		{
			name:       "internal errors stay generic",
			err:        wrap(&pgconn.PgError{Code: "XX000", Message: "could not open file \"base/1/2\""}),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "internal server error",
			wantCode:   "XX000",
		},
		{
			name:       "missing function is 404 without the schema name",
			err:        fmt.Errorf("%w: %q in schema %q", ErrRPCFunctionNotFound, "nope", "tenant_abc"),
			wantStatus: http.StatusNotFound,
			wantMsg:    `function "nope" not found`,
		},
		{
			name:       "invalid name is 400",
			err:        fmt.Errorf("%w: %q", errRPCInvalidName, "a;b"),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "invalid function name",
		},
		{
			name:       "project login unavailable is 503",
			err:        ErrTenantLoginUnavailable,
			wantStatus: http.StatusServiceUnavailable,
			wantMsg:    "the project's database is not available right now",
		},
		{
			name:       "other errors stay generic",
			err:        context.DeadlineExceeded,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "internal server error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "nope"
			got := rpcErrorFor(tc.err, name)
			if got.Status != tc.wantStatus || got.Message != tc.wantMsg || got.Code != tc.wantCode {
				t.Fatalf("got %+v, want status %d message %q code %q", got, tc.wantStatus, tc.wantMsg, tc.wantCode)
			}
		})
	}
}

// TestCallFunctionErrors_RealDB checks the mapping against errors a real
// Postgres raises from inside an RPC function (skips without a database).
func TestCallFunctionErrors_RealDB(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	q := quoteIdent(schema)
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE FUNCTION %s.rpc_err_raise() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'not your mission' USING DETAIL = 'secret-detail'; END $$`, q),
		fmt.Sprintf(`CREATE FUNCTION %s.rpc_err_div(n int) RETURNS int LANGUAGE sql AS $$ SELECT 1 / n $$`, q),
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("create function: %v", err)
		}
	}

	cases := []struct {
		fn         string
		args       map[string]interface{}
		wantStatus int
		wantMsg    string
		wantCode   string
	}{
		{"rpc_err_raise", nil, http.StatusBadRequest, "not your mission", "P0001"},
		{"rpc_err_div", map[string]interface{}{"n": 0}, http.StatusBadRequest, "invalid value for column type", "22012"},
		{"rpc_err_div", map[string]interface{}{"wrong": 1}, http.StatusBadRequest, "", "42883"},
		{"rpc_err_missing", nil, http.StatusNotFound, `function "rpc_err_missing" not found`, ""},
	}
	for _, tc := range cases {
		_, err := engine.CallFunction(ctx, schema, tc.fn, tc.args)
		if err == nil {
			t.Fatalf("%s: expected an error", tc.fn)
		}
		got := rpcErrorFor(err, tc.fn)
		if got.Status != tc.wantStatus || got.Code != tc.wantCode || (tc.wantMsg != "" && got.Message != tc.wantMsg) {
			t.Errorf("%s %v: got %+v, want status %d message %q code %q", tc.fn, tc.args, got, tc.wantStatus, tc.wantMsg, tc.wantCode)
		}
	}
}
