package cron

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func dryRunRequest(projectID string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"action_type":"sql","action":"DELETE FROM t"}`))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", projectID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
}

func TestHandleTest_Limits(t *testing.T) {
	h := handleTest(&CronService{}, NewExecutor(nil, nil))

	// Not configured.
	w := httptest.NewRecorder()
	handleTest(&CronService{}, nil)(w, dryRunRequest("p1"))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("no executor: status %d, want 503", w.Code)
	}

	// One dry run per project at a time.
	dryRunProject.Store("busy", struct{}{})
	w = httptest.NewRecorder()
	h(w, dryRunRequest("busy"))
	dryRunProject.Delete("busy")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("project busy: status %d, want 429", w.Code)
	}

	// Global slots exhausted.
	for i := 0; i < dryRunGlobal; i++ {
		dryRunSlots <- struct{}{}
	}
	w = httptest.NewRecorder()
	h(w, dryRunRequest("p2"))
	for i := 0; i < dryRunGlobal; i++ {
		<-dryRunSlots
	}
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("slots full: status %d, want 429", w.Code)
	}
	if _, stuck := dryRunProject.Load("p2"); stuck {
		t.Error("project marker not released after 429")
	}
}
