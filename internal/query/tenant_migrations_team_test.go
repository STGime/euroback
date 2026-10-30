package query

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A project with a dedicated database (Team) is refused before anything
// runs: the executor only reaches the shared cluster. The refusal comes
// before the executor's own checks, so it holds even when migrations are
// otherwise configured or not.
func TestTenantMigrations_RefusedForTeamProject(t *testing.T) {
	h := HandleTenantMigrations(nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"version":1,"name":"x","sql":"CREATE TABLE t (id int)"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "00000000-0000-0000-0000-000000000001")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(WithDedicatedDB(ctx))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "Team projects") {
		t.Fatalf("Team project migration: got %d %s, want 501 refusal", w.Code, w.Body.String())
	}

	// Without the dedicated marker the request reaches the executor
	// checks (here: not configured → 503), i.e. Free / Pro are unaffected.
	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, rctx))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusServiceUnavailable {
		t.Fatalf("shared-cluster project: got %d %s, want the executor's 503", w2.Code, w2.Body.String())
	}
}
