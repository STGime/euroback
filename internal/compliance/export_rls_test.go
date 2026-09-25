package compliance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/jackc/pgx/v5/pgxpool"
)

// #654: tables whose RLS hides rows from the runtime role must still be
// exported in full, and anything that can't be must be reported — never
// left out silently. Runs against a database built by
// scripts/db/apply-migrations.sh, with the production roles:
//
//	EXPORT_TEST_DEV_URL=postgres://eurobase_developer:localdev@localhost:5464/eurobase?sslmode=disable \
//	EXPORT_TEST_GATEWAY_URL=postgres://eurobase_gateway:localdev@localhost:5464/eurobase?sslmode=disable \
//	go test ./internal/compliance/ -run RLS
func TestTenantExport_RLSTablesAreComplete(t *testing.T) {
	devURL, gwURL := os.Getenv("EXPORT_TEST_DEV_URL"), os.Getenv("EXPORT_TEST_GATEWAY_URL")
	if devURL == "" || gwURL == "" {
		t.Skip("EXPORT_TEST_DEV_URL / EXPORT_TEST_GATEWAY_URL not set")
	}
	ctx := context.Background()
	dev := mustPool(t, devURL)
	gw := mustPool(t, gwURL)
	projectID, schema, userID := provisionRLSFixture(t, dev)
	// Production source: the developer pool as eurobase_developer itself.
	src := ExportSource{Pool: dev}

	t.Run("reads every row as the owner; FORCE RLS table is reported, not dropped", func(t *testing.T) {
		res, entries, meta := runTenantExport(t, gw, src, schema, projectID)
		for file, want := range map[string]int{
			"tables/bookings.json":        3, // <schema>_ddl-owned, owner-only policy
			"tables/series_log.json":      2, // migrator-owned, service-only policy
			"tables/user_identities.json": 1, // platform table
			"tables/users.json":           1,
			"tables/empty_table.json":     0, // empty tables get a file
			"tables/legacy_dev.json":      3, // eurobase_developer-owned (legacy MCP DDL)
			"tables/legacy_gw.json":       2, // eurobase_gateway-owned (legacy SDK DDL)
		} {
			if got := jsonRows(t, entries, file); got != want {
				t.Errorf("%s: %d rows, want %d", file, got, want)
			}
		}
		if _, ok := entries["tables/forced.json"]; ok {
			t.Error("FORCE RLS table produced a file; the read should have been refused")
		}
		if _, ok := entries["tables/a_.._b.json"]; !ok {
			t.Errorf("table named %q not exported under a safe name; have %v", "a/../b", keys(entries))
		}
		forced := findTable(meta.Tables, "forced")
		if forced == nil || forced.Status != TableFailed || !strings.Contains(forced.Error, "row-level security") {
			t.Errorf("forced table status = %+v, want failed with the RLS error", forced)
		}
		if res.Complete || meta.Complete {
			t.Error("export with a failed table reported complete")
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], `"forced"`) {
			t.Errorf("warnings = %q", res.Warnings)
		}
		if b := findTable(meta.Tables, "bookings"); b == nil || b.Status != TableExported || b.Rows != 3 || b.File != "tables/bookings.json" {
			t.Errorf("bookings = %+v", b)
		}
		// Credential columns of platform tables: rows kept, columns
		// redacted, and the redaction stated in _metadata.json.
		for table, col := range map[string]string{"users": "password_hash", "refresh_tokens": "token_hash"} {
			rows := jsonObjects(t, entries, "tables/"+table+".json")
			if len(rows) != 1 {
				t.Errorf("%s: %d rows, want 1", table, len(rows))
				continue
			}
			if _, leaked := rows[0][col]; leaked {
				t.Errorf("%s.%s exported: %v", table, col, rows[0])
			}
			if te := findTable(meta.Tables, table); te == nil || len(te.RedactedColumns) != 1 || te.RedactedColumns[0] != col {
				t.Errorf("%s redacted_columns = %+v, want [%s]", table, te, col)
			}
		}
		if u := jsonObjects(t, entries, "tables/users.json"); len(u) == 1 && u[0]["email"] != "member@export.test" {
			t.Errorf("users row lost its other columns: %v", u[0])
		}
	})

	// Why the source is eurobase_developer and not SET ROLE
	// eurobase_migrator: the migrator doesn't inherit eurobase_developer,
	// so developer-owned tables would be refused.
	t.Run("SET ROLE eurobase_migrator would lose developer-owned tables", func(t *testing.T) {
		_, _, meta := runTenantExport(t, gw, ExportSource{Pool: dev, Role: "eurobase_migrator"}, schema, projectID)
		if d := findTable(meta.Tables, "legacy_dev"); d == nil || d.Status != TableFailed {
			t.Errorf("legacy_dev as migrator = %+v, want failed", d)
		}
	})

	// The runtime role can't read owner-only tables; that must show up as
	// failures, not as tables with no rows (the pre-#654 behaviour).
	t.Run("a runtime-role source reports RLS tables as failed, never empty", func(t *testing.T) {
		res, entries, meta := runTenantExport(t, gw, ExportSource{Pool: gw}, schema, projectID)
		if _, ok := entries["tables/bookings.json"]; ok {
			t.Error("bookings exported via the gateway role — expected a refused read")
		}
		if b := findTable(meta.Tables, "bookings"); b == nil || b.Status != TableFailed {
			t.Errorf("bookings via gateway = %+v, want failed", b)
		}
		if res.Complete {
			t.Error("gateway-sourced export reported complete")
		}
	})

	t.Run("truncation is reported", func(t *testing.T) {
		old := maxRowsPerTable
		maxRowsPerTable = 2
		defer func() { maxRowsPerTable = old }()
		res, entries, meta := runTenantExport(t, gw, src, schema, projectID)
		if got := jsonRows(t, entries, "tables/bookings.json"); got != 2 {
			t.Errorf("truncated bookings: %d rows, want 2", got)
		}
		if b := findTable(meta.Tables, "bookings"); b == nil || b.Status != TableTruncated {
			t.Errorf("bookings = %+v, want truncated", b)
		}
		if s := findTable(meta.Tables, "series_log"); s == nil || s.Status != TableExported {
			t.Errorf("series_log (exactly the cap) = %+v, want exported", s)
		}
		if res.Complete || meta.RowLimitPerTable != 2 {
			t.Errorf("complete=%v row_limit_per_table=%d", res.Complete, meta.RowLimitPerTable)
		}
	})

	t.Run("complete when every table is readable", func(t *testing.T) {
		migratorExec(t, dev, fmt.Sprintf(`DROP TABLE %s.forced`, quoteIdent(schema)))
		res, _, meta := runTenantExport(t, gw, src, schema, projectID)
		if !res.Complete || !meta.Complete || len(res.Warnings) != 0 {
			t.Errorf("complete=%v meta.complete=%v warnings=%q, want complete", res.Complete, meta.Complete, res.Warnings)
		}
	})

	t.Run("user export includes the subject's RLS-protected rows and audit entries", func(t *testing.T) {
		// An end user shows up in audit_log as a target (actor_id is a
		// platform user, FK). The per-user audit query compares the id to
		// uuid actor_id and text target_id; it failed on every run before
		// #654, so this entry never reached a DSAR export.
		audit.NewService(gw).Log(ctx, projectID, "", "admin@export.test", "export.test.target", audit.WithTarget("user", userID))
		var buf bytes.Buffer
		res, err := WriteUserExport(ctx, gw, src, &buf, schema, projectID, userID, "exp-user", "json")
		if err != nil {
			t.Fatalf("WriteUserExport: %v", err)
		}
		entries := unzip(t, buf.Bytes())
		if p := jsonObjects(t, entries, "_user_profile.json"); len(p) != 1 {
			t.Errorf("profile: %d rows, want 1", len(p))
		} else if _, leaked := p[0]["password_hash"]; leaked {
			t.Errorf("DSAR profile includes password_hash: %v", p[0])
		}
		if got := jsonRows(t, entries, "tables/user_identities.json"); got != 1 {
			t.Errorf("user_identities: %d rows, want 1", got)
		}
		if got := jsonRows(t, entries, "tables/bookings.json"); got != 1 {
			t.Errorf("bookings: %d rows for this user, want 1", got)
		}
		if got := jsonRows(t, entries, "_audit_log.json"); got != 1 {
			t.Errorf("_audit_log.json: %d entries for this user, want 1", got)
		}
		if !res.Complete {
			t.Errorf("user export incomplete: %q", res.Warnings)
		}
		// integer user_id can't hold the subject's id: not a user table.
		if _, ok := entries["tables/scores.json"]; ok || findTable(res2Tables(t, entries), "scores") != nil {
			t.Error("user export included a table whose user_id is an integer")
		}
	})

	t.Run("completeness is recorded on the export request", func(t *testing.T) {
		svc := NewExportService(gw, nil, nil)
		req, err := svc.CreateExportRequest(ctx, projectID, nil, "json", userID, "platform")
		if err != nil {
			t.Fatal(err)
		}
		want := &ExportResult{Complete: false, Warnings: []string{`table "x" was truncated at 2 rows`}}
		if err := svc.MarkCompleted(ctx, req.ID, "exports/k.zip", 10, want); err != nil {
			t.Fatal(err)
		}
		got, err := svc.GetExportRequest(ctx, req.ID, projectID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Complete == nil || *got.Complete || len(got.Warnings) != 1 || got.Warnings[0] != want.Warnings[0] {
			t.Errorf("stored complete=%v warnings=%q", got.Complete, got.Warnings)
		}
		list, err := svc.ListExports(ctx, projectID, 10, 0)
		if err != nil || len(list) == 0 || list[0].Complete == nil {
			t.Errorf("ListExports: %v %+v", err, list)
		}
	})
}

// provisionRLSFixture provisions a tenant the way CreateProject does and
// adds tables covering each owner the export must handle.
func provisionRLSFixture(t *testing.T, dev *pgxpool.Pool) (projectID, schema, userID string) {
	t.Helper()
	var id string
	if err := dev.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	projectID, userID = id, "11111111-2222-3333-4444-555555555555"
	schema = "tenant_" + strings.ReplaceAll(id, "-", "_")
	ddl := schema + "_ddl"
	s, d := quoteIdent(schema), quoteIdent(ddl)
	migratorExec(t, dev,
		`INSERT INTO platform_users (id, email) VALUES ('`+id+`', '`+id+`@export.test') ON CONFLICT DO NOTHING`,
		`INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ('`+id+`', '`+id+`', 'export', 'export-`+id[:8]+`', 'tmp', 'b-`+id[:8]+`', 'fr-par', 'free', 'provisioning')`,
		`SELECT provision_tenant('`+id+`', 'export', 'free')`,
		// Tenant-migration tables are owned by <schema>_ddl.
		`SET LOCAL ROLE `+d,
		`CREATE TABLE `+s+`.bookings (id serial PRIMARY KEY, user_id uuid, name text)`,
		`ALTER TABLE `+s+`.bookings ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY own ON `+s+`.bookings USING (user_id = public.current_end_user_id())`,
		`INSERT INTO `+s+`.bookings (user_id, name) VALUES ('`+userID+`', 'a'), (gen_random_uuid(), 'b'), (gen_random_uuid(), 'c')`,
		`CREATE TABLE `+s+`.forced (id int)`,
		`ALTER TABLE `+s+`.forced ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE `+s+`.forced FORCE ROW LEVEL SECURITY`,
		`CREATE TABLE `+s+`.empty_table (id int)`,
		`CREATE TABLE `+s+`."a/../b" (id int)`,
		// SQL-editor tables are owned by eurobase_migrator.
		`SET LOCAL ROLE eurobase_migrator`,
		`CREATE TABLE `+s+`.series_log (id int, note text)`,
		`ALTER TABLE `+s+`.series_log ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY svc ON `+s+`.series_log USING (public.is_service_role())`,
		`INSERT INTO `+s+`.series_log VALUES (1, 'x'), (2, 'y')`,
		`INSERT INTO `+s+`.users (id, email, password_hash) VALUES ('`+userID+`', 'member@export.test', '$2a$12$not.a.real.hash')`,
		`INSERT INTO `+s+`.refresh_tokens (user_id, token_hash, expires_at) VALUES ('`+userID+`', 'hash-of-a-refresh-token', now() + interval '1 day')`,
		`INSERT INTO `+s+`.user_identities (user_id, provider, provider_user_id) VALUES ('`+userID+`', 'google', 'g1')`,
		// Legacy owners: MCP DDL once ran as eurobase_developer, SDK DDL
		// as eurobase_gateway (#40–#42); 000063 never reassigned those.
		`GRANT CREATE ON SCHEMA `+s+` TO eurobase_developer`,
		`RESET ROLE`,
		`CREATE TABLE `+s+`.legacy_dev (id int)`,
		`ALTER TABLE `+s+`.legacy_dev ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY p ON `+s+`.legacy_dev USING (false)`,
		`INSERT INTO `+s+`.legacy_dev VALUES (1), (2), (3)`,
		`SET LOCAL ROLE eurobase_gateway`,
		`CREATE TABLE `+s+`.legacy_gw (id int)`,
		`ALTER TABLE `+s+`.legacy_gw ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY p ON `+s+`.legacy_gw USING (false)`,
		`INSERT INTO `+s+`.legacy_gw VALUES (1), (2)`,
		// A user_id that is some other id (integer).
		`SET LOCAL ROLE `+d,
		`CREATE TABLE `+s+`.scores (id serial, user_id integer, points int)`,
		`INSERT INTO `+s+`.scores (user_id, points) VALUES (7, 10)`,
	)
	t.Cleanup(func() {
		// Best effort: audit rows are append-only, so the project row may
		// have to stay behind in the throwaway database.
		for _, stmt := range []string{
			`DROP SCHEMA IF EXISTS ` + s + ` CASCADE`,
			`DELETE FROM export_requests WHERE project_id = '` + id + `'`,
			`DELETE FROM projects WHERE id = '` + id + `'`,
		} {
			if _, err := dev.Exec(context.Background(), `SET ROLE eurobase_migrator; `+stmt+`; RESET ROLE`); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
	})
	return projectID, schema, userID
}

// migratorExec runs statements in one transaction as eurobase_migrator.
func migratorExec(t *testing.T, dev *pgxpool.Pool, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := dev.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, s := range append([]string{"SET LOCAL ROLE eurobase_migrator"}, stmts...) {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("%v\nstmt: %s", err, s)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func runTenantExport(t *testing.T, platform *pgxpool.Pool, src ExportSource, schema, projectID string) (*ExportResult, map[string][]byte, ExportMetadata) {
	t.Helper()
	var buf bytes.Buffer
	res, err := WriteTenantExport(context.Background(), platform, src, &buf, schema, projectID, "exp-rls", "json")
	if err != nil {
		t.Fatalf("WriteTenantExport: %v", err)
	}
	entries := unzip(t, buf.Bytes())
	var meta ExportMetadata
	if err := json.Unmarshal(entries["_metadata.json"], &meta); err != nil {
		t.Fatalf("_metadata.json: %v", err)
	}
	return res, entries, meta
}

func unzip(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = body
	}
	return out
}

func jsonRows(t *testing.T, entries map[string][]byte, name string) int {
	t.Helper()
	body, ok := entries[name]
	if !ok {
		t.Errorf("missing %s; have %v", name, keys(entries))
		return -1
	}
	var rows []any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Errorf("%s: %v", name, err)
		return -1
	}
	return len(rows)
}

// res2Tables returns the tables list from an archive's _metadata.json.
func res2Tables(t *testing.T, entries map[string][]byte) []TableExport {
	t.Helper()
	var meta ExportMetadata
	if err := json.Unmarshal(entries["_metadata.json"], &meta); err != nil {
		t.Fatalf("_metadata.json: %v", err)
	}
	return meta.Tables
}

func jsonObjects(t *testing.T, entries map[string][]byte, name string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(entries[name], &rows); err != nil {
		t.Errorf("%s: %v", name, err)
	}
	return rows
}

func findTable(ts []TableExport, name string) *TableExport {
	for i := range ts {
		if ts[i].Table == name {
			return &ts[i]
		}
	}
	return nil
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
