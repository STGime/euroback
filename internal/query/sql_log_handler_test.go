package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/eurobase/euroback/internal/sqllog"
	"github.com/jackc/pgx/v5/pgconn"
)

// captureWriter records SQL log inserts: statement (arg 6), outcome (10),
// detail (11).
type captureWriter struct {
	mu   sync.Mutex
	rows [][]any
}

func (c *captureWriter) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, args)
	return pgconn.CommandTag{}, nil
}

func logCtx(w *captureWriter) context.Context {
	ctx := ContextWithSchema(context.Background(), "tenant_probe")
	ctx = ContextWithProjectID(ctx, "5e1a0c9d-0000-4000-8000-000000000001")
	return sqllog.WithContext(ctx, sqllog.NewWithWriter(w))
}

// A statement the checks refuse on the console path is logged as refused,
// with the reason; the same request on the SDK path isn't logged at all.
func TestPlatformSQL_RefusedStatementIsLogged(t *testing.T) {
	engine := NewQueryEngine(nil) // never reached on the refusal path
	const stmt = "SELECT * FROM public.platform_users"

	w := &captureWriter{}
	body, _ := json.Marshal(SQLRequest{SQL: stmt})
	req := httptest.NewRequest(http.MethodPost, "/platform/x/data/sql", bytes.NewReader(body)).WithContext(logCtx(w))
	rr := httptest.NewRecorder()
	HandlePlatformSQL(engine).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	if len(w.rows) != 1 {
		t.Fatalf("want 1 log row, got %d", len(w.rows))
	}
	if w.rows[0][6] != stmt || w.rows[0][10] != sqllog.OutcomeRefused || w.rows[0][11] == "" {
		t.Errorf("row = statement %q outcome %q detail %q", w.rows[0][6], w.rows[0][10], w.rows[0][11])
	}

	w2 := &captureWriter{}
	req = httptest.NewRequest(http.MethodPost, "/v1/db/sql", bytes.NewReader(body)).WithContext(logCtx(w2))
	HandleSQL(engine).ServeHTTP(httptest.NewRecorder(), req)
	if len(w2.rows) != 0 {
		t.Errorf("SDK path must not write the SQL log, got %d rows", len(w2.rows))
	}
}

// A multi-statement request is logged as a whole: the refused statement
// with the reason, the others as not run.
func TestPlatformSQLTransaction_RefusalLogsEveryStatement(t *testing.T) {
	engine := NewQueryEngine(nil)
	w := &captureWriter{}
	stmts := []string{"CREATE TABLE a (id int)", "SELECT * FROM public.platform_users", "SELECT 1"}
	body, _ := json.Marshal(SQLTransactionRequest{Statements: stmts})
	req := httptest.NewRequest(http.MethodPost, "/platform/x/data/sql/transaction", bytes.NewReader(body)).WithContext(logCtx(w))
	rr := httptest.NewRecorder()
	HandlePlatformSQLTransaction(engine).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	// One INSERT for the request, 15 columns per statement.
	if len(w.rows) != 1 || len(w.rows[0]) != 15*len(stmts) {
		t.Fatalf("want one batched insert of %d rows, got %d inserts", len(stmts), len(w.rows))
	}
	col := func(i, c int) any { return w.rows[0][i*15+c] }
	want := []string{sqllog.OutcomeNotRun, sqllog.OutcomeRefused, sqllog.OutcomeNotRun}
	for i := range stmts {
		if col(i, 6) != stmts[i] || col(i, 10) != want[i] {
			t.Errorf("row %d = %q %q, want outcome %q", i, col(i, 6), col(i, 10), want[i])
		}
	}
	if d := col(0, 11).(string); d != "statement 2 was refused" {
		t.Errorf("statement 1 detail = %q", d)
	}
	if d := col(1, 11).(string); d == "" || d == "statement 2 was refused" {
		t.Errorf("statement 2 should carry the reason, got %q", d)
	}
}

// Outcomes map to input positions: a blank element has no result, a
// failing statement is named by its input index, and an error outside a
// statement (setup, commit) applies to all of them.
func TestTxLogOutcome(t *testing.T) {
	// Input: 0 "SELECT 1", 1 "" (skipped by the engine), 2 "UPDATE …", 3 failing, 4 never run.
	ran := resultsByIndex([]StatementResult{
		{Index: 0, RowCount: 1, ExecutionTimeMs: 2},
		{Index: 2, RowCount: 7, ExecutionTimeMs: 3},
	})
	stmtErr := statementError(3, errors.New("relation \"nope\" does not exist"))

	cases := []struct {
		i       int
		err     error
		outcome string
		detail  string
		hasDur  bool
	}{
		{0, nil, sqllog.OutcomeOK, "", true},
		{1, nil, sqllog.OutcomeOK, "", false},
		{0, stmtErr, sqllog.OutcomeError, "rolled back: statement 4 failed", true},
		{1, stmtErr, sqllog.OutcomeError, "rolled back: statement 4 failed", false},
		{2, stmtErr, sqllog.OutcomeError, "rolled back: statement 4 failed", true},
		{3, stmtErr, sqllog.OutcomeError, stmtErr.Error(), false},
		{4, stmtErr, sqllog.OutcomeNotRun, "statement 4 failed", false},
		{2, errors.New("commit: could not serialize access"), sqllog.OutcomeError, "transaction not committed: commit: could not serialize access", true},
	}
	for _, c := range cases {
		outcome, detail, dur, _ := txLogOutcome(c.i, ran, c.err)
		if outcome != c.outcome || detail != c.detail || (dur != nil) != c.hasDur {
			t.Errorf("i=%d err=%v: got %q %q dur=%v", c.i, c.err, outcome, detail, dur != nil)
		}
	}
	if _, _, _, rows := txLogOutcome(2, ran, nil); rows == nil || *rows != 7 {
		t.Error("row count should come from the matching result, not the slice position")
	}
	// The message is unchanged for callers.
	if stmtErr.Error() != `statement 3: relation "nope" does not exist` {
		t.Errorf("StatementError message = %q", stmtErr.Error())
	}
}
