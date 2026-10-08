package query

import (
	"bytes"
	"context"
	"encoding/json"
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
	if len(w.rows) != len(stmts) {
		t.Fatalf("want %d log rows, got %d", len(stmts), len(w.rows))
	}
	for i, row := range w.rows {
		if row[6] != stmts[i] || row[10] != sqllog.OutcomeRefused {
			t.Errorf("row %d = %q %q", i, row[6], row[10])
		}
	}
	if d := w.rows[0][11].(string); d != "not run: statement 2 was refused" {
		t.Errorf("statement 1 detail = %q", d)
	}
	if d := w.rows[1][11].(string); d == "" || d == "not run: statement 2 was refused" {
		t.Errorf("statement 2 should carry the reason, got %q", d)
	}
}
