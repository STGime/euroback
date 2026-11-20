package enduser

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// #814: the SDK signs the user out only on 400/401/403 from
// /v1/auth/refresh and keeps the session on a 5xx, so the gateway must not
// answer 401 for its own failures.
func TestRefreshErrorResponse(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{ErrInvalidRefreshToken, http.StatusUnauthorized},
		{fmt.Errorf("wrapped: %w", ErrInvalidRefreshToken), http.StatusUnauthorized},
		{ErrEmailNotConfirmed, http.StatusForbidden},
		{ErrAccountSuspended, http.StatusForbidden},
		{fmt.Errorf("wrapped: %w", ErrAccountSuspended), http.StatusForbidden},
		{errors.New("begin tx: connection refused"), http.StatusInternalServerError},
		{fmt.Errorf("commit: %w", errors.New("timeout")), http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got, _ := refreshErrorResponse(c.err); got != c.want {
			t.Errorf("refreshErrorResponse(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// GET /v1/auth/user answers 403 account_suspended for a suspended user, so
// a server that checks a still-valid access token learns about it.
func TestGetUserErrorResponse(t *testing.T) {
	cases := []struct {
		err      error
		want     int
		wantCode string
	}{
		{ErrAccountSuspended, http.StatusForbidden, "account_suspended"},
		{fmt.Errorf("wrapped: %w", ErrAccountSuspended), http.StatusForbidden, "account_suspended"},
		{errors.New("user not found"), http.StatusNotFound, ""},
		{errors.New("query user: connection refused"), http.StatusInternalServerError, ""},
	}
	for _, c := range cases {
		got, body := getUserErrorResponse(c.err)
		if got != c.want || body["code"] != c.wantCode {
			t.Errorf("getUserErrorResponse(%v) = %d %v, want %d code %q", c.err, got, body, c.want, c.wantCode)
		}
		if body["error"] == "" {
			t.Errorf("getUserErrorResponse(%v): empty error", c.err)
		}
	}
}
