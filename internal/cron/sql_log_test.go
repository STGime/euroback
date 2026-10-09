package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/eurobase/euroback/internal/sqllog"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type captureLog struct {
	mu   sync.Mutex
	rows [][]any
}

func (c *captureLog) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, args)
	return pgconn.CommandTag{}, nil
}

func cronReq(t *testing.T, body any, w *captureLog) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", "5e1a0c9d-0000-4000-8000-000000000002")
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	return r.WithContext(sqllog.WithContext(ctx, sqllog.NewWithWriter(w)))
}

// A cron job whose request the checks refuse still lands in the SQL log
// (source cron, outcome refused); a non-SQL job doesn't.
func TestCronCreate_RefusedSQLIsLogged(t *testing.T) {
	w := &captureLog{}
	rr := httptest.NewRecorder()
	handleCreate(nil).ServeHTTP(rr, cronReq(t, map[string]any{
		"name": "", "schedule": "* * * * *", "action_type": "sql", "action": "DELETE FROM todos",
	}, w))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	if len(w.rows) != 1 {
		t.Fatalf("want 1 log row, got %d", len(w.rows))
	}
	if w.rows[0][5] != sqllog.SourceCron || w.rows[0][6] != "DELETE FROM todos" || w.rows[0][10] != sqllog.OutcomeRefused {
		t.Errorf("row = source %v statement %v outcome %v", w.rows[0][5], w.rows[0][6], w.rows[0][10])
	}

	w2 := &captureLog{}
	handleCreate(nil).ServeHTTP(httptest.NewRecorder(), cronReq(t, map[string]any{
		"name": "", "schedule": "* * * * *", "action_type": "function", "action": "nightly",
	}, w2))
	if len(w2.rows) != 0 {
		t.Errorf("a function job has no SQL to log, got %d rows", len(w2.rows))
	}
}
