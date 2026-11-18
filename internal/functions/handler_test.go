package functions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// writeGetError used to be `jsonError(w, "function not found", 404)` for
// every error path from Service.Get — which masked vault decryption
// failures (#206 sealed env_vars) and any other real failure behind a
// 404 lie. The console then displayed "Function not found" on every
// function the user clicked, because the JS layer takes the backend
// error message verbatim.
//
// These two tests pin the new contract:
//   * pgx.ErrNoRows  →  404 "function not found"   (real not-found)
//   * anything else  →  500 with the err.Error()    (real reason surfaced)

func TestWriteGetError_RealNotFoundIs404(t *testing.T) {
	w := httptest.NewRecorder()
	wrapped := fmt.Errorf("get edge function %q: %w", "hello", pgx.ErrNoRows)
	writeGetError(w, "proj-1", "hello", wrapped)

	if w.Code != 404 {
		t.Errorf("expected 404 for ErrNoRows, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "function not found") {
		t.Errorf("expected 'function not found' message, got %q", w.Body.String())
	}
}

func TestWriteGetError_OtherErrorSurfacesAs500(t *testing.T) {
	w := httptest.NewRecorder()
	// Simulate the post-#206 decryption-failure path: an opaque internal
	// error that has NOTHING to do with the row being missing. The user
	// must see the real reason or they cannot diagnose.
	wrapped := errors.New("decrypt env_vars (key_version 1): cipher: message authentication failed")
	writeGetError(w, "proj-1", "hello", wrapped)

	if w.Code != 500 {
		t.Errorf("expected 500 for non-not-found error, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "function not found") {
		t.Errorf("must not mask non-not-found error as 'function not found', body=%q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "decrypt env_vars") {
		t.Errorf("real error reason missing from response body: %q", w.Body.String())
	}
}

func TestCheckCodeSize(t *testing.T) {
	if err := checkCodeSize(strings.Repeat("a", MaxCodeBytes)); err != nil {
		t.Errorf("exactly the limit: %v", err)
	}
	if err := checkCodeSize(strings.Repeat("a", MaxCodeBytes+1)); !errors.Is(err, ErrCodeTooLarge) {
		t.Errorf("over the limit: %v", err)
	}
	if fnErrStatus(ErrCodeTooLarge) != http.StatusRequestEntityTooLarge || fnErrStatus(errors.New("x")) != http.StatusBadRequest {
		t.Error("fnErrStatus mapping")
	}
}

// Create refuses oversized code before it reaches esbuild or the database.
func TestServiceCreate_CodeTooLarge(t *testing.T) {
	svc := &Service{}
	_, err := svc.Create(context.Background(), "p", CreateRequest{Name: "f", Code: strings.Repeat("a", MaxCodeBytes+1)})
	if !errors.Is(err, ErrCodeTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestHandleCreateUpdate_BodyTooLarge(t *testing.T) {
	big := `{"name":"f","code":"` + strings.Repeat("a", maxFunctionBody) + `"}`
	for name, h := range map[string]http.HandlerFunc{
		"create": HandleCreate(nil, nil),
		"update": HandleUpdate(nil),
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big)))
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: want 413, got %d: %s", name, rr.Code, rr.Body.String())
		}
	}
}
