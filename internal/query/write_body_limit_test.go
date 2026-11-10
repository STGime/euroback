package query

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Data API writes refuse a body over maxWriteBodyBytes with 413 before
// decoding it (the engine here has no pool, so nothing else runs).
func TestWriteBodyLimit(t *testing.T) {
	big := `{"s":"` + strings.Repeat("x", maxWriteBodyBytes) + `"}`
	cases := []struct {
		name   string
		h      http.HandlerFunc
		method string
		params map[string]string
	}{
		{"insert", handleInsertRow(&QueryEngine{}, nil), http.MethodPost, map[string]string{"table": "items"}},
		{"update", handleUpdateRow(&QueryEngine{}, nil), http.MethodPatch, map[string]string{"table": "items", "id": "1"}},
		{"bulk delete", HandleTableBulkDelete(&QueryEngine{}, nil), http.MethodPost, map[string]string{"table": "items"}},
		{"rpc", handleCallFunction(&QueryEngine{}), http.MethodPost, map[string]string{"function": "f"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "/", strings.NewReader(big))
			rctx := chi.NewRouteContext()
			for k, v := range c.params {
				rctx.URLParams.Add(k, v)
			}
			ctx := ContextWithSchema(req.Context(), "tenant_x")
			req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()
			c.h(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}
