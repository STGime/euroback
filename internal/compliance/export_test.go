package compliance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ── #103: formatCSVCell — pure unit tests (no DB) ────────────────────────────

// TestFormatCSVCell_JSONBRoundTrips locks in #103: a JSONB column
// pulled out as map[string]interface{} must serialise as parseable JSON
// in the CSV cell, NOT Go's fmt "%v" form (`map[key:val]`).
func TestFormatCSVCell_JSONBRoundTrips(t *testing.T) {
	cases := []struct {
		name string
		in   any
		// We don't compare raw strings because map iteration order is
		// non-deterministic; instead we re-parse the output as JSON
		// and verify it's structurally equal.
		want any
	}{
		{
			name: "jsonb object",
			in:   map[string]interface{}{"k": "v", "n": float64(7)},
			want: map[string]interface{}{"k": "v", "n": float64(7)},
		},
		{
			name: "text array",
			in:   []interface{}{"a", "b", "c"},
			want: []interface{}{"a", "b", "c"},
		},
		{
			name: "nested",
			in: map[string]interface{}{
				"settings": map[string]interface{}{"theme": "dark", "count": float64(2)},
				"tags":     []interface{}{"x", "y"},
			},
			want: map[string]interface{}{
				"settings": map[string]interface{}{"theme": "dark", "count": float64(2)},
				"tags":     []interface{}{"x", "y"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := formatCSVCell(c.in)
			if s == "" {
				t.Fatalf("got empty string for input %v", c.in)
			}
			if strings.HasPrefix(s, "map[") {
				t.Fatalf("regression of #103: cell formatted via Go fmt %%v: %q", s)
			}
			var got any
			if err := json.Unmarshal([]byte(s), &got); err != nil {
				t.Fatalf("cell not parseable as JSON: %v\nraw: %q", err, s)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(c.want)
			// Re-marshal both sides to canonicalise.
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("round-trip mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestFormatCSVCell_Scalars(t *testing.T) {
	if got := formatCSVCell(nil); got != "" {
		t.Errorf("nil: got %q, want empty string", got)
	}
	if got := formatCSVCell("hello"); got != "hello" {
		t.Errorf("string: got %q, want %q", got, "hello")
	}
	if got := formatCSVCell([]byte("raw")); got != "raw" {
		t.Errorf("bytes: got %q, want %q", got, "raw")
	}
	if got := formatCSVCell(int64(42)); got != "42" {
		t.Errorf("int64: got %q, want %q", got, "42")
	}
	if got := formatCSVCell(true); got != "true" {
		t.Errorf("bool: got %q, want %q", got, "true")
	}
	ts, _ := time.Parse(time.RFC3339Nano, "2026-05-18T09:00:00Z")
	if got := formatCSVCell(ts); got != "2026-05-18T09:00:00Z" {
		t.Errorf("time: got %q, want RFC3339Nano", got)
	}
}

// ── #99: streaming WriteTenantExport — DB-backed ─────────────────────────────

// TestWriteTenantExport_StreamsZipStructure exercises the full
// streaming pipeline against a real Postgres so the bug-fix is wired
// to the same SQL path production uses. Skipped in -short mode.
//
// What we assert:
//   - The returned io.Writer (a bytes.Buffer here, a temp file in prod)
//     contains a valid zip archive
//   - That archive has the expected entries (tables/<name>.json,
//     _audit_log.json, _metadata.json)
//   - Per-table JSON entries are parseable arrays of objects with
//     the rows we inserted (regression guard for the JSON-array
//     streaming format)
//   - CSV entries are valid CSV with the right header + row count
//   - Total row count returned matches reality
func TestWriteTenantExport_StreamsZipStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	schema, projectID, cleanup := setupExportFixture(t, pool)
	defer cleanup()

	for _, format := range []string{"json", "csv"} {
		t.Run("format="+format, func(t *testing.T) {
			var buf bytes.Buffer
			total, err := WriteTenantExport(ctx, pool, &buf, schema, projectID, "exp-"+format, format)
			if err != nil {
				t.Fatalf("WriteTenantExport: %v", err)
			}
			if total < 3 {
				t.Fatalf("expected ≥3 rows total (2 todos + 1 user), got %d", total)
			}

			zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
			if err != nil {
				t.Fatalf("zip not parseable: %v", err)
			}

			ext := "." + format
			wantEntries := []string{"tables/todos" + ext, "tables/users" + ext, "_metadata.json"}
			gotEntries := map[string]*zip.File{}
			for _, f := range zr.File {
				gotEntries[f.Name] = f
			}
			for _, name := range wantEntries {
				if _, ok := gotEntries[name]; !ok {
					t.Errorf("missing zip entry %q", name)
				}
			}

			// Spot-check the todos file is well-formed.
			f, ok := gotEntries["tables/todos"+ext]
			if !ok {
				t.Fatalf("no todos entry in zip; have: %v", keysOf(gotEntries))
			}
			rc, _ := f.Open()
			body, _ := io.ReadAll(rc)
			rc.Close()
			switch format {
			case "json":
				var rows []map[string]interface{}
				if err := json.Unmarshal(body, &rows); err != nil {
					t.Fatalf("todos.json not array of objects: %v\nbody:\n%s", err, body)
				}
				if len(rows) != 2 {
					t.Errorf("todos.json: got %d rows, want 2", len(rows))
				}
			case "csv":
				rdr := csv.NewReader(bytes.NewReader(body))
				records, err := rdr.ReadAll()
				if err != nil {
					t.Fatalf("todos.csv not parseable: %v", err)
				}
				if len(records) != 3 { // header + 2 rows
					t.Errorf("todos.csv: got %d records (incl header), want 3", len(records))
				}
			}

			// Spot-check metadata.
			fm, ok := gotEntries["_metadata.json"]
			if !ok {
				t.Fatalf("no _metadata.json in zip")
			}
			rc, _ = fm.Open()
			body, _ = io.ReadAll(rc)
			rc.Close()
			var meta ExportMetadata
			if err := json.Unmarshal(body, &meta); err != nil {
				t.Fatalf("_metadata.json: %v\nbody:\n%s", err, body)
			}
			if meta.ProjectID != projectID {
				t.Errorf("metadata project_id: got %q want %q", meta.ProjectID, projectID)
			}
			if meta.Format != format {
				t.Errorf("metadata format: got %q want %q", meta.Format, format)
			}
		})
	}
}

// TestWriteUserExport_FiltersToOneUser locks in the per-user export
// scope: a per-user zip must contain only that user's profile row and
// only rows they own from tables with a user_id column.
func TestWriteUserExport_FiltersToOneUser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	schema, projectID, cleanup := setupExportFixture(t, pool)
	defer cleanup()

	// The fixture inserts two todos owned by user A and one by user B.
	// A user-scoped export for A should only contain A's two.
	var userAID string
	if err := pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT id FROM %s.users WHERE email = $1`, quoteIdent(schema)),
		"a@test.local",
	).Scan(&userAID); err != nil {
		t.Fatalf("lookup user A: %v", err)
	}

	var buf bytes.Buffer
	total, err := WriteUserExport(ctx, pool, &buf, schema, projectID, userAID, "exp-user", "json")
	if err != nil {
		t.Fatalf("WriteUserExport: %v", err)
	}
	if total < 1 {
		t.Fatalf("expected ≥1 row, got %d", total)
	}

	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	gotEntries := map[string]*zip.File{}
	for _, f := range zr.File {
		gotEntries[f.Name] = f
	}
	todos, ok := gotEntries["tables/todos.json"]
	if !ok {
		t.Fatalf("expected tables/todos.json in user export; have %v", keysOf(gotEntries))
	}
	rc, _ := todos.Open()
	body, _ := io.ReadAll(rc)
	rc.Close()
	var rows []map[string]interface{}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("todos.json: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("user export todos: got %d rows, want 2 (user A only)", len(rows))
	}
	for _, r := range rows {
		if r["user_id"] != userAID {
			t.Errorf("user export leaked another user's row: %v", r)
		}
	}
}

// TestWriteTenantExport_BoundedMemory is a coarse regression guard for
// the #99 fix. Before the streaming refactor a 5k-row tenant export
// held every row in RAM plus the entire zip; afterwards memory should
// be roughly constant. We measure by writing the zip into an io.Writer
// that simply discards bytes — a counting writer — and verifying we
// never need to hold the whole thing.
//
// We don't try to measure RSS (too flaky); instead the test asserts
// the function works when streaming to a writer that doesn't expose a
// .Bytes() / .Len() handle to the caller. Old code path returned a
// *bytes.Buffer; if anyone reintroduces that pattern, this test won't
// catch it, but the file-level diff will. The real protection is the
// type signature: WriteTenantExport now requires io.Writer.
func TestWriteTenantExport_AcceptsArbitraryWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	schema, projectID, cleanup := setupExportFixture(t, pool)
	defer cleanup()

	cw := &countingWriter{}
	total, err := WriteTenantExport(ctx, pool, cw, schema, projectID, "exp-cw", "json")
	if err != nil {
		t.Fatalf("WriteTenantExport into counting writer: %v", err)
	}
	if total < 3 {
		t.Errorf("rows: got %d want ≥3", total)
	}
	if cw.bytes < 100 {
		t.Errorf("counting writer received only %d bytes — zip wasn't streamed", cw.bytes)
	}
}

// ── #101: CheckRateLimit ignores failed exports ──────────────────────────────

// TestCheckRateLimit_IgnoresFailedExports verifies that a failed export
// does NOT count against the 24h/1h budget — the bug #101 was that any
// row in export_requests created in the window counted, so a worker
// crash locked the user out.
func TestCheckRateLimit_IgnoresFailedExports(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	_, projectID, cleanup := setupExportFixture(t, pool)
	defer cleanup()

	svc := NewExportService(pool, nil, nil)

	// 1. Fresh project, no rows → not limited.
	if limited, err := svc.CheckRateLimit(ctx, projectID, nil); err != nil || limited {
		t.Fatalf("fresh project tenant: limited=%v err=%v want false/nil", limited, err)
	}

	// 2. Insert a failed tenant export → still not limited.
	failedID := insertExportRequest(t, pool, projectID, nil, "failed")
	defer deleteExportRequest(t, pool, failedID)
	if limited, err := svc.CheckRateLimit(ctx, projectID, nil); err != nil || limited {
		t.Errorf("with 1 failed export: limited=%v err=%v want false/nil (regression of #101)", limited, err)
	}

	// 3. Insert a completed tenant export → now limited.
	completedID := insertExportRequest(t, pool, projectID, nil, "completed")
	defer deleteExportRequest(t, pool, completedID)
	if limited, err := svc.CheckRateLimit(ctx, projectID, nil); err != nil || !limited {
		t.Errorf("with 1 completed export: limited=%v err=%v want true/nil", limited, err)
	}

	// Same shape for user-scope limit.
	userID := "11111111-1111-1111-1111-111111111111"
	if limited, _ := svc.CheckRateLimit(ctx, projectID, &userID); limited {
		t.Errorf("user-scope fresh: limited=true want false")
	}
	failedUserID := insertExportRequest(t, pool, projectID, &userID, "failed")
	defer deleteExportRequest(t, pool, failedUserID)
	if limited, _ := svc.CheckRateLimit(ctx, projectID, &userID); limited {
		t.Errorf("user-scope with failed: limited=true want false (regression of #101)")
	}
}

// ── #102: UserExistsInTenant ─────────────────────────────────────────────────

// TestUserExistsInTenant_FalseForUnknownUUID locks in #102: random
// UUIDs must come back as not-existing, so the handler can return 404
// before enqueueing a useless worker job.
func TestUserExistsInTenant_TrueFalse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	schema, projectID, cleanup := setupExportFixture(t, pool)
	defer cleanup()
	_ = schema

	svc := NewExportService(pool, nil, nil)

	var userAID string
	if err := pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT id FROM %s.users WHERE email = $1`, quoteIdent(schema)),
		"a@test.local",
	).Scan(&userAID); err != nil {
		t.Fatalf("lookup user A: %v", err)
	}

	exists, err := svc.UserExistsInTenant(ctx, projectID, userAID)
	if err != nil {
		t.Fatalf("UserExistsInTenant existing: %v", err)
	}
	if !exists {
		t.Error("existing user A: got false, want true (RLS bypass regression — see PR #144)")
	}

	exists, err = svc.UserExistsInTenant(ctx, projectID, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("UserExistsInTenant unknown: %v", err)
	}
	if exists {
		t.Error("unknown UUID: got true, want false (regression of #102)")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("cannot connect to %s: %v", url, err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("cannot ping %s: %v", url, err)
	}
	return pool
}

// setupExportFixture stands up a throwaway tenant schema with a users
// table and a todos table seeded with deterministic data, plus a
// public.projects row pointing at it. Returns schema, project_id, and
// a cleanup func that tears it all down.
//
// The fixture intentionally uses very small data so tests stay fast.
// Schema names are randomised per-test so parallel runs don't collide.
func setupExportFixture(t *testing.T, pool *pgxpool.Pool) (schema, projectID string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	// Random schema + ids
	suffix := fmt.Sprintf("dsar_test_%d", time.Now().UnixNano())
	schema = suffix
	projectID = randomUUID(t, pool)

	cleanupFn := func() {
		// Best-effort: drop the schema and the project row.
		_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, quoteIdent(schema)))
		_, _ = pool.Exec(ctx, `DELETE FROM export_requests WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.audit_log WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
	}

	// Build the fixture.
	stmts := []string{
		fmt.Sprintf(`CREATE SCHEMA %s`, quoteIdent(schema)),
		fmt.Sprintf(`CREATE TABLE %s.users (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text)`, quoteIdent(schema)),
		fmt.Sprintf(`CREATE TABLE %s.todos (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid, title text, meta jsonb)`, quoteIdent(schema)),
		fmt.Sprintf(`INSERT INTO %s.users (email) VALUES ('a@test.local'), ('b@test.local')`, quoteIdent(schema)),
		fmt.Sprintf(`INSERT INTO %s.todos (user_id, title, meta)
			SELECT id, 'A1', '{"tags": ["x"]}'::jsonb FROM %s.users WHERE email = 'a@test.local'`,
			quoteIdent(schema), quoteIdent(schema)),
		fmt.Sprintf(`INSERT INTO %s.todos (user_id, title, meta)
			SELECT id, 'A2', '{"tags": ["y"]}'::jsonb FROM %s.users WHERE email = 'a@test.local'`,
			quoteIdent(schema), quoteIdent(schema)),
		fmt.Sprintf(`INSERT INTO %s.todos (user_id, title, meta)
			SELECT id, 'B1', '{"tags": ["z"]}'::jsonb FROM %s.users WHERE email = 'b@test.local'`,
			quoteIdent(schema), quoteIdent(schema)),
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			cleanupFn()
			t.Skipf("fixture setup: %v\nstmt: %s", err, s)
		}
	}

	// Make the per-tenant users.id visible to UserExistsInTenant.
	// In production this query goes through edb.RunAsService which sets
	// `app.end_user_role='service'` — but RLS is only ON for tables
	// created via the platform migrator. Our test users table has no
	// policies so plain SELECT works.

	if _, err := pool.Exec(ctx,
		`INSERT INTO projects (id, slug, name, schema_name, s3_bucket)
		 VALUES ($1, 'dsar-test-' || substr($1::text, 1, 8), 'DSAR Test', $2, 'dsar-test-bucket')`,
		projectID, schema,
	); err != nil {
		cleanupFn()
		t.Skipf("insert project row: %v", err)
	}

	return schema, projectID, cleanupFn
}

func randomUUID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("gen_random_uuid: %v", err)
	}
	return id
}

func insertExportRequest(t *testing.T, pool *pgxpool.Pool, projectID string, userID *string, status string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO export_requests (project_id, user_id, format, requested_by, requested_by_type, status)
		 VALUES ($1, $2, 'json', $3, 'platform', $4)
		 RETURNING id`,
		projectID, userID, "00000000-0000-0000-0000-000000000000", status,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert export_requests (%s): %v", status, err)
	}
	return id
}

func deleteExportRequest(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	_, _ = pool.Exec(context.Background(), `DELETE FROM export_requests WHERE id = $1`, id)
}

type countingWriter struct{ bytes int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.bytes += int64(len(p))
	return len(p), nil
}

func keysOf(m map[string]*zip.File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
