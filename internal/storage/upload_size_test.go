package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

func TestCheckUploadSize(t *testing.T) {
	const mb = int64(1) << 20
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/storage/signed-url", nil)
		return r.WithContext(auth.ContextWithProject(r.Context(), &auth.ProjectContext{ProjectID: "p1"}))
	}
	limit := func(n int64, err error) UploadLimitFunc {
		return func(_ context.Context, id string) (int64, error) {
			if id != "p1" {
				t.Errorf("limit looked up for project %q", id)
			}
			return n, err
		}
	}
	for _, tc := range []struct {
		name   string
		h      *StorageHandler
		size   int64
		status int
		body   string
	}{
		{"fits", (&StorageHandler{}).WithUploadLimit(limit(500*mb, nil)), 500 * mb, 0, ""},
		{"too large", (&StorageHandler{}).WithUploadLimit(limit(500*mb, nil)), 500*mb + 1, http.StatusRequestEntityTooLarge, "upload limit of 500 MB"},
		{"lookup fails", (&StorageHandler{}).WithUploadLimit(limit(0, errors.New("db down"))), 1, http.StatusServiceUnavailable, "unavailable"},
		{"no limit wired", &StorageHandler{}, 1, http.StatusServiceUnavailable, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := tc.h.checkUploadSize(req(), tc.size)
			if status != tc.status || !strings.Contains(body, tc.body) {
				t.Errorf("got %d %q, want %d containing %q", status, body, tc.status, tc.body)
			}
		})
	}
}
