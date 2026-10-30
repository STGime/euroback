package query

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A Team project (dedicated database) whose owner pool isn't on the
// request (instance provisioning, restoring or unreachable) is answered
// 503 before anything runs — never the shared cluster's executor. Without
// the dedicated marker the request takes the shared-cluster path as
// before (here: not configured → its own 503).
func TestTenantMigrations_TeamWithoutPoolRefused(t *testing.T) {
	h := HandleTenantMigrations(nil, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "00000000-0000-0000-0000-000000000001")

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"version":1,"name":"x","sql":"CREATE TABLE t (id int)"}`))
	req = req.WithContext(WithDedicatedDB(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "dedicated database") {
		t.Fatalf("Team project without its pool: got %d %s, want 503 (dedicated database unavailable)", w.Code, w.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, rctx))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusServiceUnavailable || !strings.Contains(w2.Body.String(), "DDL_PASSWORD_SECRET") {
		t.Fatalf("shared-cluster project: got %d %s, want the shared executor's 503", w2.Code, w2.Body.String())
	}
}

func TestMigrationExecutor_DedicatedEnabled(t *testing.T) {
	var nilExec *MigrationExecutor
	if nilExec.DedicatedEnabled() {
		t.Fatal("nil executor must not be enabled")
	}
	if (&MigrationExecutor{}).DedicatedEnabled() {
		t.Fatal("executor without a bookkeeping pool must not be enabled")
	}
}
