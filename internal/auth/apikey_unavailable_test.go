package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// #814: when the key lookup fails because the database can't be reached,
// the answer is 503, not 401 — the SDK ends an end user's session on a 401
// from /v1/auth/refresh, which sits behind this middleware.
func TestAPIKeyMiddleware_DatabaseDownIs503(t *testing.T) {
	// Nothing listens on port 1: every query fails to connect.
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1&sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	reached := false
	h := NewAPIKeyMiddleware(pool).Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", nil)
	r.Header.Set("apikey", "eb_pk_test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || reached {
		t.Fatalf("status = %d (reached=%v), want 503", w.Code, reached)
	}

	if _, err := ResolveAPIKey(context.Background(), pool, "eb_pk_test"); err == nil || errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("ResolveAPIKey = %v, want a non-ErrInvalidAPIKey error", err)
	}
}
