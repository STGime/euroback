package k8sapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretGetAndPatch(t *testing.T) {
	store := map[string]string{"userlist": base64.StdEncoding.EncodeToString([]byte("old"))}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v1/namespaces/eurobase/secrets/pgb" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"data": store}) //nolint:errcheck
		case http.MethodPatch:
			if r.Header.Get("Content-Type") != "application/merge-patch+json" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
			var p struct {
				Data map[string]string `json:"data"`
			}
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &p) //nolint:errcheck
			for k, v := range p.Data {
				store[k] = v
			}
			w.Write([]byte("{}")) //nolint:errcheck
		}
	}))
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte("tok\n"), 0o600) //nolint:errcheck

	c := New(srv.URL, tok, "eurobase", srv.Client().Transport.(*http.Transport).TLSClientConfig)
	ctx := context.Background()
	if err := c.PatchSecretData(ctx, "pgb", map[string][]byte{"userlist": []byte("new"), "upstream": []byte("h:5432/db")}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetSecretData(ctx, "pgb")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["userlist"]) != "new" || string(got["upstream"]) != "h:5432/db" {
		t.Errorf("got %q", got)
	}
	if _, err := c.GetSecretData(ctx, "other"); err != ErrNotFound {
		t.Errorf("other secret: err = %v, want ErrNotFound", err)
	}
}
