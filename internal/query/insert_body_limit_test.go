package query

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The insert body is capped before it is decoded: an oversized body is a
// 413 without touching the database (the engine here has no pool).
func TestInsertBodyLimit(t *testing.T) {
	h := handleInsertRow(&QueryEngine{}, nil)
	body := "[" + strings.Repeat(`{"name":"`+strings.Repeat("x", 1<<20)+`"},`, 17) + `{}]`
	req := httptest.NewRequest(http.MethodPost, "/v1/db/items", bytes.NewBufferString(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("table", "items")
	ctx := ContextWithSchema(req.Context(), "tenant_x")
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body.String())
	}
}
