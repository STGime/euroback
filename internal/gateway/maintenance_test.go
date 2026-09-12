package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

func TestMaintenanceModeMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	handler := MaintenanceModeMiddleware(next)

	t.Run("no project context passes through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/db/foo", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
	})

	t.Run("maintenance false passes through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/db/foo", nil)
		pc := &auth.ProjectContext{ProjectID: "p1", MaintenanceMode: false}
		req = req.WithContext(auth.ContextWithProject(req.Context(), pc))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
	})

	t.Run("maintenance true returns 503 with retry-after", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/db/foo", nil)
		pc := &auth.ProjectContext{ProjectID: "p1", MaintenanceMode: true}
		req = req.WithContext(auth.ContextWithProject(req.Context(), pc))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d", rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got != "60" {
			t.Fatalf("want Retry-After 60, got %q", got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("want Content-Type application/json, got %q", got)
		}
	})
}
