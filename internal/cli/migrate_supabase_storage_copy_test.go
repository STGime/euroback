package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fakeSource serves files from memory.
type fakeSource struct {
	files map[string][]byte // key → bytes
	// serveSize, when set, is the size reported instead of the real one.
	serveSize map[string]int64
	fail      map[string]bool
}

func (s *fakeSource) Open(_ context.Context, f sourceFile) (io.ReadCloser, int64, error) {
	if s.fail[f.Key()] {
		return nil, 0, errors.New("HTTP 500 storage error")
	}
	b, ok := s.files[f.Key()]
	if !ok {
		return nil, 0, errors.New("HTTP 404 not found")
	}
	size := int64(len(b))
	if n, ok := s.serveSize[f.Key()]; ok {
		size = n
	}
	return io.NopCloser(bytes.NewReader(b)), size, nil
}

// fakeTarget is the project: its records, its bucket (an upload server)
// and its shared folders, with the platform's ownership rules.
type fakeTarget struct {
	mu      sync.Mutex
	users   map[string]bool
	records map[string]string // key → owner ("" = developer file)
	bucket  map[string][]byte
	shared  []string
	server  *httptest.Server
	putFail map[string]bool
	calls   int
}

func newFakeTarget(t *testing.T, users ...string) *fakeTarget {
	ft := &fakeTarget{users: map[string]bool{}, records: map[string]string{}, bucket: map[string][]byte{}, putFail: map[string]bool{}}
	for _, u := range users {
		ft.users[u] = true
	}
	ft.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		if r.Method != http.MethodPut || r.Header.Get("X-Signed") != "yes" || r.ContentLength < 0 {
			http.Error(w, "bad upload request", http.StatusBadRequest)
			return
		}
		ft.mu.Lock()
		fail := ft.putFail[key]
		ft.mu.Unlock()
		if fail {
			http.Error(w, "SlowDown", http.StatusServiceUnavailable)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if int64(len(b)) != r.ContentLength {
			http.Error(w, "short body", http.StatusBadRequest)
			return
		}
		ft.mu.Lock()
		ft.bucket[key] = b
		ft.mu.Unlock()
	}))
	t.Cleanup(ft.server.Close)
	return ft
}

func (ft *fakeTarget) Import(items []importItem) ([]importAnswer, error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.calls++
	if len(items) > 100 {
		return nil, fmt.Errorf("batch of %d", len(items))
	}
	var out []importAnswer
	for _, it := range items {
		owner, recorded := ft.records[it.Key]
		switch {
		case it.Owner != "" && !ft.users[it.Owner]:
			out = append(out, importAnswer{Key: it.Key, Status: "error", Code: "unknown_owner", Error: "not a user"})
		case recorded && owner != it.Owner:
			out = append(out, importAnswer{Key: it.Key, Status: "conflict", Error: "another owner"})
		case recorded && int64(len(ft.bucket[it.Key])) == it.Size && ft.bucket[it.Key] != nil:
			out = append(out, importAnswer{Key: it.Key, Status: "exists"})
		default:
			ft.records[it.Key] = it.Owner
			out = append(out, importAnswer{Key: it.Key, Status: "upload", URL: ft.server.URL + "/" + it.Key, Headers: map[string]string{"X-Signed": "yes"}})
		}
	}
	return out, nil
}

func (ft *fakeTarget) Complete(keys []string) ([]completeAnswer, error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var out []completeAnswer
	for _, k := range keys {
		if b, ok := ft.bucket[k]; ok {
			out = append(out, completeAnswer{Key: k, Status: "done", Size: int64(len(b))})
		} else {
			out = append(out, completeAnswer{Key: k, Status: "missing"})
		}
	}
	return out, nil
}

func (ft *fakeTarget) Share(prefix, visibility string) error {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.shared = append(ft.shared, prefix+"="+visibility)
	return nil
}

// memQuerier answers listSourceFiles from a fixed list, as the source
// database would (byte order, after a cursor, a limit).
func copyRun(t *testing.T, files []sourceFile, src *fakeSource, dst *fakeTarget, buckets []supabaseBucket, missingOwner string) (string, *copyStats, error) {
	t.Helper()
	stats := &copyStats{}
	var out bytes.Buffer
	opts := copyOptions{Parallel: 3, MissingOwner: missingOwner, Upload: dst.server.Client(), PublicBuckets: map[string]bool{}}
	for _, b := range buckets {
		if b.public {
			opts.PublicBuckets[b.name] = true
			_ = dst.Share(b.name+"/", "public")
		}
	}
	// Batches of two, to cross batch borders.
	for i := 0; i < len(files); i += 2 {
		end := min(i+2, len(files))
		if err := copyFiles(context.Background(), files[i:end], src, dst, opts, stats); err != nil {
			return out.String(), stats, err
		}
	}
	return out.String(), stats, nil
}

func TestCopyFiles_OwnersPublicBucketsAndResume(t *testing.T) {
	const alice, bob = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	files := []sourceFile{
		{Bucket: "avatars", Name: "a/me.png", Size: 5, ContentType: "image/png", Owner: alice},
		{Bucket: "avatars", Name: "b/me.png", Size: 3, ContentType: "image/png", Owner: bob},
		{Bucket: "docs", Name: "x y/ä.pdf", Size: 4, ContentType: "application/pdf"},
		{Bucket: "public", Name: "logo.svg", Size: 2, ContentType: "image/svg+xml", Owner: alice},
		{Bucket: "avatars", Name: "c/me.png", Size: 1, Owner: "33333333-3333-3333-3333-333333333333"},
	}
	src := &fakeSource{files: map[string][]byte{
		"avatars/a/me.png": []byte("alice"), "avatars/b/me.png": []byte("bob"), "docs/x y/ä.pdf": []byte("docs"),
		"public/logo.svg": []byte("<>"), "avatars/c/me.png": []byte("c"),
	}}
	dst := newFakeTarget(t, alice, bob)
	buckets := []supabaseBucket{{name: "avatars"}, {name: "docs"}, {name: "public", public: true}}

	_, stats, err := copyRun(t, files, src, dst, buckets, "developer")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Copied != 5 || stats.Failed != 0 || stats.Bytes != 15 {
		t.Fatalf("copied=%d failed=%d bytes=%d problems=%v", stats.Copied, stats.Failed, stats.Bytes, stats.Problems)
	}
	// Owners kept; the public bucket's file and the unknown owner's file
	// are developer files; the public bucket's folder is shared.
	want := map[string]string{"avatars/a/me.png": alice, "avatars/b/me.png": bob, "docs/x y/ä.pdf": "", "public/logo.svg": "", "avatars/c/me.png": ""}
	for k, o := range want {
		if dst.records[k] != o || string(dst.bucket[k]) != string(src.files[k]) {
			t.Errorf("%s: owner %q bytes %q, want owner %q", k, dst.records[k], dst.bucket[k], o)
		}
	}
	if stats.OwnerDropped != 2 || strings.Join(dst.shared, ",") != "public/=public" {
		t.Errorf("owners dropped %d, shared %v", stats.OwnerDropped, dst.shared)
	}

	// Run again: nothing is copied twice.
	_, again, err := copyRun(t, files, src, dst, buckets, "developer")
	if err != nil {
		t.Fatal(err)
	}
	if again.Copied != 0 || again.Already != 5 || again.Bytes != 0 {
		t.Errorf("second run: copied=%d already=%d problems=%v", again.Copied, again.Already, again.Problems)
	}
}

func TestCopyFiles_MissingOwnerSkip(t *testing.T) {
	files := []sourceFile{{Bucket: "b", Name: "f", Size: 1, Owner: "33333333-3333-3333-3333-333333333333"}}
	dst := newFakeTarget(t)
	_, stats, err := copyRun(t, files, &fakeSource{files: map[string][]byte{"b/f": []byte("x")}}, dst, nil, "skip")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Copied != 0 || stats.Skipped != 1 || len(dst.bucket) != 0 || !strings.Contains(strings.Join(stats.Problems, "\n"), "not a user of the project") {
		t.Errorf("skip: %+v", stats)
	}
}

func TestCopyFiles_FailuresAreCountedAndRetriable(t *testing.T) {
	files := []sourceFile{
		{Bucket: "b", Name: "ok", Size: 2},
		{Bucket: "b", Name: "source-down", Size: 2},
		{Bucket: "b", Name: "wrong-size", Size: 9},
		{Bucket: "b", Name: "upload-down", Size: 2},
		{Bucket: "b", Name: "taken", Size: 2},
	}
	src := &fakeSource{
		files:     map[string][]byte{"b/ok": []byte("ok"), "b/source-down": []byte("..."), "b/wrong-size": []byte("ab"), "b/upload-down": []byte("up"), "b/taken": []byte("tk")},
		fail:      map[string]bool{"b/source-down": true},
		serveSize: map[string]int64{"b/wrong-size": 2},
	}
	dst := newFakeTarget(t)
	dst.putFail["b/upload-down"] = true
	dst.records["b/taken"] = "44444444-4444-4444-4444-444444444444"
	_, stats, err := copyRun(t, files, src, dst, nil, "developer")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Copied != 1 || stats.Failed != 3 || stats.Skipped != 1 {
		t.Fatalf("copied=%d failed=%d skipped=%d problems=%v", stats.Copied, stats.Failed, stats.Skipped, stats.Problems)
	}
	problems := strings.Join(stats.Problems, "\n")
	for _, want := range []string{"b/source-down: read from Supabase", "b/wrong-size: the source serves 2 bytes, its records say 9", "b/upload-down: upload: HTTP 503", "b/taken: another owner"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems lack %q:\n%s", want, problems)
		}
	}
	// The file that belongs to someone else was not touched.
	if dst.records["b/taken"] != "44444444-4444-4444-4444-444444444444" || dst.bucket["b/taken"] != nil {
		t.Error("a file of another owner was overwritten")
	}
	// Fixed on the next run.
	dst.putFail["b/upload-down"] = false
	src.fail["b/source-down"] = false
	src.files["b/source-down"] = []byte("..")
	_, again, err := copyRun(t, files, src, dst, nil, "developer")
	if err != nil {
		t.Fatal(err)
	}
	if again.Copied != 2 || again.Already != 1 {
		t.Errorf("second run: %+v", again)
	}
}

func TestSupabaseStorageSource_Request(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Length", "3")
		_, _ = w.Write([]byte("abc"))
	}))
	defer srv.Close()
	src := &supabaseStorageSource{base: srv.URL + "/", key: "service-key", client: srv.Client()}
	body, size, err := src.Open(context.Background(), sourceFile{Bucket: "my-bucket", Name: "a b/c?d#e%f/ü.txt"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body)
	body.Close()
	if string(b) != "abc" || size != 3 {
		t.Errorf("body %q size %d", b, size)
	}
	if got.URL.EscapedPath() != "/storage/v1/object/my-bucket/a%20b/c%3Fd%23e%25f/%C3%BC.txt" {
		t.Errorf("path %s", got.URL.EscapedPath())
	}
	if got.Header.Get("Authorization") != "Bearer service-key" || got.Header.Get("apikey") != "service-key" {
		t.Errorf("headers %v", got.Header)
	}
}

// With a database: the files are listed in byte order across buckets,
// owner from either column, sizes as recorded, and placeholders left out.
func TestListSourceFiles_Fixture(t *testing.T) {
	dsn := os.Getenv("ASSESS_SCRATCH_DB_URL")
	if dsn == "" {
		t.Skip("set ASSESS_SCRATCH_DB_URL to a throwaway database to run this")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		DROP SCHEMA IF EXISTS storage CASCADE;
		CREATE SCHEMA storage;
		CREATE TABLE storage.buckets (id text PRIMARY KEY, public boolean DEFAULT false, file_size_limit bigint, allowed_mime_types text[]);
		CREATE TABLE storage.objects (id uuid DEFAULT gen_random_uuid(), bucket_id text, name text, owner uuid, owner_id text, metadata jsonb);
		INSERT INTO storage.buckets (id, public) VALUES ('avatars', false), ('Zeta', true), ('other', false);
		INSERT INTO storage.objects (bucket_id, name, owner, owner_id, metadata) VALUES
			('avatars', 'b.png', '11111111-1111-1111-1111-111111111111', NULL, '{"size": 5, "mimetype": "image/png"}'),
			('avatars', 'B.png', NULL, '22222222-2222-2222-2222-222222222222', '{"size": "6"}'),
			('avatars', 'a/.emptyFolderPlaceholder', NULL, NULL, '{"size": 0}'),
			('Zeta', 'z', NULL, NULL, NULL),
			('other', 'not listed', NULL, NULL, '{"size": 1}');
	`); err != nil {
		t.Fatal(err)
	}
	var got []string
	after, afterName := "", ""
	for {
		files, err := listSourceFiles(ctx, tx, []string{"avatars", "Zeta"}, after, afterName, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			break
		}
		for _, f := range files {
			got = append(got, fmt.Sprintf("%s|%d|%s|%s", f.Key(), f.Size, f.ContentType, f.Owner))
		}
		after, afterName = files[len(files)-1].Bucket, files[len(files)-1].Name
	}
	want := []string{
		"Zeta/z|-1||",
		"avatars/B.png|6||22222222-2222-2222-2222-222222222222",
		"avatars/a/.emptyFolderPlaceholder|0||",
		"avatars/b.png|5|image/png|11111111-1111-1111-1111-111111111111",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
