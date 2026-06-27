package functions

import (
	"errors"
	"fmt"
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
