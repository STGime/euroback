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
		{errors.New("begin tx: connection refused"), http.StatusInternalServerError},
		{fmt.Errorf("commit: %w", errors.New("timeout")), http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got, _ := refreshErrorResponse(c.err); got != c.want {
			t.Errorf("refreshErrorResponse(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
