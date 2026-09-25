package storage

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// #655: compliance export archives live under exports/<project-id>/ with
// no storage_objects row. Console admins can download them (checked
// against export_requests); nobody can write or delete there through the
// storage API; the list shows them only to callers who may read them.

const testProjectID = "7d0f1c2e-0000-4000-8000-000000000655"

type caller struct {
	platform bool   // console (platform claims)
	role     string // project role on the context
	endUser  bool   // SDK end-user JWT
}

func exportCtx(ctx context.Context, c caller) context.Context {
	ctx = auth.ContextWithProject(ctx, &auth.ProjectContext{ProjectID: testProjectID, Slug: "club", SchemaName: "tenant_x"})
	if c.platform {
		ctx = auth.ContextWithClaims(ctx, &auth.Claims{Subject: "platform-user", Email: "dev@example.com"})
	}
	if c.role != "" {
		ctx = tenant.WithRole(ctx, c.role)
	}
	if c.endUser {
		ctx = auth.ContextWithEndUserClaims(ctx, &auth.EndUserClaims{UserID: "11111111-2222-3333-4444-555555555555"})
	}
	return ctx
}

func TestIsExportKey(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(exportCtx(r.Context(), caller{}))
	for key, want := range map[string]bool{
		"exports/" + testProjectID + "/e1.zip":          true,
		"exports/" + testProjectID + "/users/u1/e2.zip": true,
		"exports/" + testProjectID:                      false, // not inside the namespace
		"exports/other-project/e1.zip":                  false, // another project's shape
		"exports/e1.zip":                                false, // a tenant's own "exports/" folder
		"Exports/" + testProjectID + "/e1.zip":          false,
		"avatars/" + testProjectID + "/exports/x.png":   false,
	} {
		if got := isExportKey(r, key); got != want {
			t.Errorf("isExportKey(%q) = %v, want %v", key, got, want)
		}
	}
	// No project on the context: nothing is an export key.
	bare := httptest.NewRequest("GET", "/", nil)
	if isExportKey(bare, "exports/"+testProjectID+"/e1.zip") {
		t.Error("export key recognised without a project context")
	}
}

func TestMayReadExports(t *testing.T) {
	for name, tc := range map[string]struct {
		c    caller
		want bool
	}{
		"console admin":        {caller{platform: true, role: "admin"}, true},
		"console owner":        {caller{platform: true, role: "owner"}, true},
		"console developer":    {caller{platform: true, role: "developer"}, false},
		"console viewer":       {caller{platform: true, role: "viewer"}, false},
		"console, no role":     {caller{platform: true}, false},
		"SDK end user":         {caller{endUser: true}, false},
		"SDK end user + admin": {caller{endUser: true, role: "admin"}, false}, // role alone isn't enough
		"API key only":         {caller{}, false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(exportCtx(r.Context(), tc.c))
		if got := mayReadExports(r); got != tc.want {
			t.Errorf("%s: mayReadExports = %v, want %v", name, got, tc.want)
		}
	}
}

func TestVisibleObjects_HidesExportsFromOthers(t *testing.T) {
	objs := []ObjectInfo{
		{Key: "avatars/a.png"},
		{Key: "exports/" + testProjectID + "/e1.zip"},
		{Key: "exports/" + testProjectID + "/users/u1/e2.zip"},
		{Key: "exports/mine.csv"},
	}
	list := func(c caller) []string {
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(exportCtx(r.Context(), c))
		var keys []string
		for _, o := range visibleObjects(r, objs) {
			keys = append(keys, o.Key)
		}
		return keys
	}
	if got := list(caller{platform: true, role: "admin"}); len(got) != 4 {
		t.Errorf("admin sees %v, want all 4", got)
	}
	for name, c := range map[string]caller{
		"viewer":   {platform: true, role: "viewer"},
		"end user": {endUser: true},
	} {
		got := list(c)
		if strings.Join(got, ",") != "avatars/a.png,exports/mine.csv" {
			t.Errorf("%s sees %v, want only avatars/a.png and exports/mine.csv", name, got)
		}
	}
}

// Writes into the namespace are refused before any S3 call (the handler
// has no S3 client here — reaching it would panic).
func TestExportNamespace_WritesRefused(t *testing.T) {
	h := &StorageHandler{}
	key := "exports/" + testProjectID + "/e1.zip"
	for name, c := range map[string]caller{
		"console admin": {platform: true, role: "owner"},
		"SDK end user":  {endUser: true},
	} {
		t.Run(name+"/upload", func(t *testing.T) {
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			_ = mw.WriteField("key", key)
			fw, _ := mw.CreateFormFile("file", "e1.zip")
			_, _ = fw.Write([]byte("PK tampered"))
			_ = mw.Close()
			r := httptest.NewRequest("POST", "/upload", &body)
			r.Header.Set("Content-Type", mw.FormDataContentType())
			r = r.WithContext(exportCtx(r.Context(), c))
			w := httptest.NewRecorder()
			h.UploadFile(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("upload into exports/: %d %s, want 403", w.Code, w.Body)
			}
		})
		t.Run(name+"/upload URL", func(t *testing.T) {
			r := httptest.NewRequest("POST", "/signed-url", strings.NewReader(`{"key":"`+key+`","operation":"upload"}`))
			r = r.WithContext(exportCtx(r.Context(), c))
			w := httptest.NewRecorder()
			h.GenerateSignedURL(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("upload URL into exports/: %d %s, want 403", w.Code, w.Body)
			}
		})
		t.Run(name+"/delete", func(t *testing.T) {
			r := httptest.NewRequest("DELETE", "/"+key, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("*", key)
			r = r.WithContext(context.WithValue(exportCtx(r.Context(), c), chi.RouteCtxKey, rctx))
			w := httptest.NewRecorder()
			h.DeleteFile(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("delete in exports/: %d %s, want 403", w.Code, w.Body)
			}
		})
	}
}

// A caller who may not read exports gets 404 for a download URL, as for
// any object they can't see (no S3 call either).
func TestExportNamespace_DownloadURLNeedsAdmin(t *testing.T) {
	h := &StorageHandler{}
	key := "exports/" + testProjectID + "/e1.zip"
	for name, c := range map[string]caller{
		"console viewer": {platform: true, role: "viewer"},
		"SDK end user":   {endUser: true},
	} {
		r := httptest.NewRequest("POST", "/signed-url", strings.NewReader(`{"key":"`+key+`","operation":"download"}`))
		r = r.WithContext(exportCtx(r.Context(), c))
		w := httptest.NewRecorder()
		h.GenerateSignedURL(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: download URL for an export: %d, want 404", name, w.Code)
		}
	}
}

// exportArchiveReadable against a real export_requests table. Needs a
// migrated database: DATABASE_URL (a role that can insert projects and
// export_requests).
func TestExportArchiveReadable_DB(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" || testing.Short() {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	const other = "7d0f1c2e-0000-4000-8000-000000000656"
	// Each value is its own parameter: one parameter used as both uuid and
	// text fails with "inconsistent types deduced".
	for _, id := range []string{testProjectID, other} {
		exec(`INSERT INTO platform_users (id, email) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, id+"@x.test")
		exec(`INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		      VALUES ($1, $1, 'p', $2, $3, $4, 'fr-par', 'free', 'active')
		      ON CONFLICT (id) DO NOTHING`, id, "p-"+id[len(id)-12:], "tenant_export_test_"+id[len(id)-3:], "b-export-test-"+id[len(id)-3:])
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id IN ($1, $2)`, testProjectID, other)
	}()
	insert := func(project, key, status, expires string) {
		exec(`INSERT INTO export_requests (project_id, format, requested_by, status, s3_key, completed_at, expires_at)
		      VALUES ($1, 'json', $1, $2, $3, now(), now() + $4::interval)`, project, status, key, expires)
	}
	pre := "exports/" + testProjectID + "/"
	insert(testProjectID, pre+"ok.zip", "completed", "7 days")
	insert(testProjectID, pre+"expired.zip", "completed", "-1 hour")
	insert(testProjectID, pre+"running.zip", "running", "7 days")
	insert(other, pre+"theirs.zip", "completed", "7 days") // another project's row

	h := &StorageHandler{pool: pool}
	check := func(c caller, key string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(exportCtx(r.Context(), c))
		ok, err := h.exportArchiveReadable(r, key)
		if err != nil {
			t.Fatalf("exportArchiveReadable(%s): %v", key, err)
		}
		return ok
	}
	admin := caller{platform: true, role: "admin"}
	for key, want := range map[string]bool{
		pre + "ok.zip":      true,
		pre + "expired.zip": false,
		pre + "running.zip": false,
		pre + "theirs.zip":  false,
		pre + "missing.zip": false,
	} {
		if got := check(admin, key); got != want {
			t.Errorf("admin, %s: readable = %v, want %v", key, got, want)
		}
	}
	if check(caller{platform: true, role: "viewer"}, pre+"ok.zip") {
		t.Error("viewer may read an export archive")
	}
}
