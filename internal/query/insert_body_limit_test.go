package query

import (
	"bytes"
	"context"
	"errors"
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

func TestDecodeInsertBody(t *testing.T) {
	obj, rows, isArray, err := decodeInsertBody(strings.NewReader(`{"a":1,"b":"x"}`))
	if err != nil || isArray || obj["b"] != "x" || rows != nil {
		t.Fatalf("object: %v %v %v %v", obj, rows, isArray, err)
	}
	obj, _, isArray, err = decodeInsertBody(strings.NewReader(`null`))
	if err != nil || isArray || obj == nil || len(obj) != 0 {
		t.Fatalf("null: %v %v %v", obj, isArray, err)
	}
	_, rows, isArray, err = decodeInsertBody(strings.NewReader(` [ {"a":1}, {} ] `))
	if err != nil || !isArray || len(rows) != 2 {
		t.Fatalf("array: %v %v %v", rows, isArray, err)
	}
	for _, bad := range []string{`[null]`, `[[1]]`, `["x"]`, `"x"`, `1`, `[{"a":1}] [{}]`, `[{"a":1}`} {
		if _, _, _, err := decodeInsertBody(strings.NewReader(bad)); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

// Reading stops at the row cap: a long array of empty objects is refused
// after MaxBulkInsertRows+1 elements, not decoded in full.
func TestDecodeInsertBody_StopsAtRowCap(t *testing.T) {
	r := &countingReader{r: strings.NewReader("[" + strings.Repeat("{},", 2_000_000) + "{}]")}
	_, _, _, err := decodeInsertBody(r)
	if !errors.Is(err, ErrBulkInsertSize) {
		t.Fatalf("err = %v, want ErrBulkInsertSize", err)
	}
	if r.n > 64<<10 {
		t.Fatalf("read %d bytes before refusing, want it to stop early", r.n)
	}
}

type countingReader struct {
	r interface{ Read([]byte) (int, error) }
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}
