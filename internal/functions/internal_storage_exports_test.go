package functions

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/storage"
)

// #655: edge functions can't write, delete or sign URLs for the project's
// compliance export archives. The handler here has no pool and no S3
// client — reaching the project lookup would panic — so a pass also shows
// the refusal happens before any database or storage call.
func TestInternalStorage_RefusesExportArchives(t *testing.T) {
	const projectID = "7d0f1c2e-0000-4000-8000-000000000655"
	key := storage.ExportArchivePrefix(projectID) + "users/u1/e1.zip"
	h := &InternalStorageHandler{hmacSecret: []byte(testStorageSecret)}

	signed := func(method, path string, op StorageOp, body []byte) *http.Request {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		for k, v := range makeStorageHeader(projectID, "tenant_x", "u1", key) {
			r.Header[k] = v
		}
		SignStorage([]byte(testStorageSecret), r.Header, op, SHA256Hex(body), time.Now())
		return r
	}

	cases := map[string]struct {
		req  *http.Request
		call func(http.ResponseWriter, *http.Request)
	}{
		"upload": {
			signed("POST", "/upload", StorageOpUpload, []byte("PK tampered")),
			h.upload,
		},
		"upload URL": {
			signed("POST", "/signed-url", StorageOpSignedURL, []byte(`{"key":"`+key+`","operation":"upload"}`)),
			h.signedURL,
		},
		"download URL": {
			signed("POST", "/signed-url", StorageOpSignedURL, []byte(`{"key":"`+key+`","operation":"download"}`)),
			h.signedURL,
		},
		"delete": {
			signed("DELETE", "/"+key, StorageOpDelete, nil),
			h.delete,
		},
	}
	for name, tc := range cases {
		w := httptest.NewRecorder()
		tc.call(w, tc.req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s of an export archive: %d %s, want 403", name, w.Code, w.Body)
		}
	}
}

func TestRefuseExportArchive_OnlyThisProjectsNamespace(t *testing.T) {
	const projectID = "7d0f1c2e-0000-4000-8000-000000000655"
	for key, refused := range map[string]bool{
		storage.ExportArchivePrefix(projectID) + "e1.zip":     true,
		"exports/7d0f1c2e-0000-4000-8000-000000000656/e1.zip": false, // another project's shape
		"exports/report.csv": false, // the tenant's own exports/ folder
		"avatars/me.png":     false,
	} {
		w := httptest.NewRecorder()
		if got := refuseExportArchive(w, projectID, key); got != refused {
			t.Errorf("refuseExportArchive(%q) = %v, want %v", key, got, refused)
		}
	}
}
