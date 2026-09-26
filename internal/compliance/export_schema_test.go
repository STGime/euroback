package compliance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeLister serves a fixed listing, pageSize objects per page.
type fakeLister struct {
	objects  []storage.ObjectInfo
	pageSize int
	err      error
}

func (f fakeLister) ListObjects(_ context.Context, _, _ string, limit int, token string) (*storage.ListResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	size := f.pageSize
	if size == 0 {
		size = limit
	}
	start := 0
	if token != "" {
		fmt.Sscanf(token, "%d", &start)
	}
	end := start + size
	if end > len(f.objects) {
		end = len(f.objects)
	}
	res := &storage.ListResult{Objects: f.objects[start:end]}
	if end < len(f.objects) {
		res.IsTruncated, res.NextToken = true, fmt.Sprint(end)
	}
	return res, nil
}

func TestRedactSecrets(t *testing.T) {
	var cfg any
	_ = json.Unmarshal([]byte(`{
		"providers": {"email": {"enabled": true}},
		"oauth_providers": {"google": {"enabled": true, "client_id": "cid", "client_secret": "leak", "secret_set": true}},
		"rate_limits": {"token_refresh_per_5min_per_ip": 30, "trust_proxy": false},
		"smtp": {"password": "leak", "api_key": "leak", "host": "mail.example"},
		"hooks": [{"url": "https://x", "token": "leak"}]
	}`), &cfg)
	var redacted []string
	out := redactSecrets(cfg, "", &redacted)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "leak") {
		t.Errorf("secret left in manifest: %s", b)
	}
	for _, keep := range []string{`"client_id":"cid"`, `"secret_set":true`, `"token_refresh_per_5min_per_ip":30`, `"host":"mail.example"`} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("%s dropped: %s", keep, b)
		}
	}
	want := []string{"hooks[0].token", "oauth_providers.google.client_secret", "smtp.api_key", "smtp.password"}
	got := append([]string(nil), redacted...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("redacted = %v, want %v", got, want)
	}
}

func TestLibpqEnv(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://dev:p%40ss@db.internal:6543/eurobase?sslmode=require&pool_max_conns=4")
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(libpqEnv(cfg), "\n")
	for _, want := range []string{"PGHOST=db.internal", "PGPORT=6543", "PGUSER=dev", "PGPASSWORD=p@ss", "PGDATABASE=eurobase", "PGSSLMODE=require", "PGPASSFILE=/dev/null"} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("missing %s in\n%s", want, env)
		}
	}
	if strings.Contains(env, "pool_max_conns") {
		t.Error("pgx-only parameter passed to libpq")
	}
	kv, _ := pgxpool.ParseConfig("host=/tmp port=5432 user=u dbname=d sslmode=disable")
	if env := strings.Join(libpqEnv(kv), " "); !strings.Contains(env, "PGSSLMODE=disable") || !strings.Contains(env, "PGHOST=/tmp") {
		t.Errorf("keyword/value config: %s", env)
	}
}

func TestStorageManifest(t *testing.T) {
	ctx := context.Background()
	pid := "7d0f1c2e-0000-4000-8000-000000000001"
	ts := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	objs := []storage.ObjectInfo{
		{Key: "avatars/a.png", Size: 10, ETag: "abc", LastModified: ts},
		{Key: storage.ExportArchivePrefix(pid) + "old.zip", Size: 999},
		{Key: "docs/b.pdf", Size: 20, ETag: "def-2", LastModified: ts},
		{Key: "untracked.txt", Size: 1},
	}
	read := func(t *testing.T, zw *bytes.Buffer) StorageManifest {
		zr, err := zip.NewReader(bytes.NewReader(zw.Bytes()), int64(zw.Len()))
		if err != nil {
			t.Fatal(err)
		}
		var m StorageManifest
		for _, f := range zr.File {
			if f.Name == storageManifestFile {
				rc, _ := f.Open()
				_ = json.NewDecoder(rc).Decode(&m)
				rc.Close()
			}
		}
		return m
	}
	run := func(opts TenantExportOptions) (SectionExport, StorageManifest) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		se := exportStorageManifest(ctx, zw, opts, pid, map[string]string{"avatars/a.png": "image/png", "docs/b.pdf": "application/pdf"})
		_ = zw.Close()
		return se, read(t, &buf)
	}

	t.Run("pages, skips export archives, joins tracking rows", func(t *testing.T) {
		se, m := run(TenantExportOptions{Objects: fakeLister{objects: objs, pageSize: 1}, Bucket: "b"})
		if se.Status != TableExported || se.Count == nil || *se.Count != 3 {
			t.Fatalf("section = %+v", se)
		}
		want := []StorageManifestEntry{
			{Key: "avatars/a.png", Size: 10, ETag: "abc", LastModified: ts, ContentType: "image/png", Tracked: true},
			{Key: "docs/b.pdf", Size: 20, ETag: "def-2", LastModified: ts, ContentType: "application/pdf", Tracked: true},
			{Key: "untracked.txt", Size: 1},
		}
		if !reflect.DeepEqual(m.Objects, want) || m.Truncated {
			t.Errorf("manifest = %+v", m)
		}
	})
	t.Run("truncated at the cap", func(t *testing.T) {
		old := maxManifestObjects
		maxManifestObjects = 2
		defer func() { maxManifestObjects = old }()
		se, m := run(TenantExportOptions{Objects: fakeLister{objects: objs, pageSize: 1}, Bucket: "b"})
		if se.Status != TableTruncated || len(m.Objects) != 2 || !m.Truncated {
			t.Errorf("section = %+v, %d objects, truncated=%v", se, len(m.Objects), m.Truncated)
		}
	})
	t.Run("exactly the cap is not truncated", func(t *testing.T) {
		old := maxManifestObjects
		maxManifestObjects = 3
		defer func() { maxManifestObjects = old }()
		if se, _ := run(TenantExportOptions{Objects: fakeLister{objects: objs, pageSize: 2}, Bucket: "b"}); se.Status != TableExported {
			t.Errorf("section = %+v", se)
		}
	})
	t.Run("unreadable tracking table is stated, not reported as untracked", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		se := exportStorageManifest(ctx, zw, TenantExportOptions{Objects: fakeLister{objects: objs}, Bucket: "b"}, pid, nil)
		_ = zw.Close()
		if m := read(t, &buf); se.Status != TableExported || !m.TrackingUnavailable {
			t.Errorf("section = %+v, tracking_unavailable = %v", se, m.TrackingUnavailable)
		}
	})
	t.Run("listing error and missing storage are failures", func(t *testing.T) {
		if se, _ := run(TenantExportOptions{Objects: fakeLister{err: errors.New("boom")}, Bucket: "b"}); se.Status != TableFailed {
			t.Errorf("listing error: %+v", se)
		}
		if se, _ := run(TenantExportOptions{}); se.Status != TableFailed {
			t.Errorf("no storage: %+v", se)
		}
	})
}

// #656: the schema section, against a database built by
// scripts/db/apply-migrations.sh (see TestTenantExport_RLSTablesAreComplete).
func TestTenantExport_SchemaSection(t *testing.T) {
	devURL, gwURL := os.Getenv("EXPORT_TEST_DEV_URL"), os.Getenv("EXPORT_TEST_GATEWAY_URL")
	if devURL == "" || gwURL == "" {
		t.Skip("EXPORT_TEST_DEV_URL / EXPORT_TEST_GATEWAY_URL not set")
	}
	ctx := context.Background()
	dev := mustPool(t, devURL)
	gw := mustPool(t, gwURL)
	projectID, schema, _ := provisionRLSFixture(t, dev)
	s := quoteIdent(schema)
	migratorExec(t, dev,
		`SET LOCAL ROLE `+quoteIdent(schema+"_ddl"),
		`CREATE TABLE `+s+`.lanes (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text CHECK (length(name) < 50))`,
		`CREATE TABLE `+s+`.slots (id serial PRIMARY KEY, lane_id bigint REFERENCES `+s+`.lanes(id) ON DELETE CASCADE ON UPDATE RESTRICT)`,
		`CREATE INDEX slots_lane_idx ON `+s+`.slots (lane_id)`,
		`CREATE FUNCTION `+s+`.touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`CREATE TRIGGER slots_touch BEFORE UPDATE ON `+s+`.slots FOR EACH ROW EXECUTE FUNCTION `+s+`.touch()`,
		`CREATE VIEW `+s+`.lane_names AS SELECT name FROM `+s+`.lanes`,
		`COMMENT ON TABLE `+s+`.lanes IS 'shooting lanes'`,
		`INSERT INTO `+s+`.lanes (name) VALUES ('one'), ('two')`,
		`RESET ROLE`,
		`UPDATE public.projects SET auth_config = '{"providers":{"email":{"enabled":true}},"oauth_providers":{"google":{"enabled":true,"client_id":"cid","client_secret":"must-not-leak"}}}' WHERE id = '`+projectID+`'`,
		`INSERT INTO public.email_templates (project_id, template_type, subject, body_html) VALUES ('`+projectID+`', 'verification', 'Welcome', '<p>hi</p>')`,
	)
	t.Cleanup(func() {
		migratorExec(t, dev, `DELETE FROM public.email_templates WHERE project_id = '`+projectID+`'`)
	})

	t.Run("schema.sql, sequences and manifests are exported from the tenant schema", func(t *testing.T) {
		migratorExec(t, dev, fmt.Sprintf(`DROP TABLE IF EXISTS %s.forced`, s))
		res, entries, meta := runTenantExport(t, gw, ExportSource{Pool: dev}, schema, projectID)
		if !res.Complete {
			t.Fatalf("export incomplete: %q", res.Warnings)
		}
		if meta.FormatVersion != ExportFormatVersion {
			t.Errorf("format_version = %d", meta.FormatVersion)
		}
		for _, name := range []string{"schema", "sequences", "auth_manifest", "storage_manifest"} {
			if sec := findSection(meta.Sections, name); sec == nil || sec.Status != TableExported {
				t.Errorf("section %s = %+v", name, sec)
			}
		}
		dump := string(entries[schemaDumpFile])
		for _, want := range []string{
			"CREATE SCHEMA " + schema + ";",
			"CREATE TABLE " + schema + ".lanes",
			"GENERATED ALWAYS AS IDENTITY",
			"CHECK ((length(name) < 50))",
			"FOREIGN KEY (lane_id) REFERENCES " + schema + ".lanes(id) ON UPDATE RESTRICT ON DELETE CASCADE",
			"CREATE INDEX slots_lane_idx",
			"ALTER TABLE " + schema + ".bookings ENABLE ROW LEVEL SECURITY",
			"CREATE POLICY own ON " + schema + ".bookings USING ((user_id = public.current_end_user_id()))",
			"CREATE POLICY svc ON " + schema + ".series_log",
			"CREATE FUNCTION " + schema + ".touch()",
			"CREATE TRIGGER slots_touch",
			"CREATE VIEW " + schema + ".lane_names",
			"COMMENT ON TABLE " + schema + ".lanes IS 'shooting lanes'",
		} {
			if !strings.Contains(dump, want) {
				t.Errorf("schema.sql lacks %q", want)
			}
		}
		// Tenant-schema objects only; no owners or grants of platform roles.
		for _, not := range []string{"OWNER TO", "GRANT ", "REVOKE ", "CREATE SCHEMA public", "eurobase_"} {
			if strings.Contains(dump, not) {
				t.Errorf("schema.sql contains %q", not)
			}
		}
		var seqs []SequenceValue
		if err := json.Unmarshal(entries[sequencesFile], &seqs); err != nil {
			t.Fatal(err)
		}
		if v := findSeq(seqs, "lanes_id_seq"); v == nil || v.LastValue != 2 || !v.IsCalled {
			t.Errorf("lanes_id_seq = %+v, want 2 (called)", v)
		}
		if v := findSeq(seqs, "slots_id_seq"); v == nil || v.IsCalled {
			t.Errorf("slots_id_seq = %+v, want not called", v)
		}
		auth := string(entries[authManifestFile])
		if strings.Contains(auth, "must-not-leak") || !strings.Contains(auth, `"client_id": "cid"`) || !strings.Contains(auth, "oauth_providers.google.client_secret") || !strings.Contains(auth, `"subject": "Welcome"`) {
			t.Errorf("auth manifest:\n%s", auth)
		}
		if _, ok := entries[storageManifestFile]; !ok {
			t.Error("no storage manifest")
		}
	})

	t.Run("the dump reads the export's snapshot", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		var se SectionExport
		err := withExportSnapshot(ctx, ExportSource{Pool: dev}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM `+s+`.lanes LIMIT 1`); err != nil {
				return err
			}
			// Created after the snapshot was taken: must not be in the dump.
			migratorExec(t, dev, `CREATE TABLE `+s+`.after_snapshot (id int)`)
			se = exportSchemaDump(ctx, tx, ExportSource{Pool: dev}, zw, schema)
			return nil
		})
		_ = zw.Close()
		if err != nil || se.Status != TableExported {
			t.Fatalf("err = %v, section = %+v", err, se)
		}
		dump := string(unzip(t, buf.Bytes())[schemaDumpFile])
		if !strings.Contains(dump, "CREATE TABLE "+schema+".lanes") || strings.Contains(dump, "after_snapshot") {
			t.Errorf("dump not taken from the export snapshot (after_snapshot present = %v)", strings.Contains(dump, "after_snapshot"))
		}
	})

	t.Run("past the cap, tracking rows are read in S3 (byte) order", func(t *testing.T) {
		migratorExec(t, dev, `INSERT INTO `+s+`.storage_objects (key, content_type) VALUES ('b', 'x/b'), ('Z', 'x/Z'), ('a', 'x/a')`)
		old := maxManifestObjects
		maxManifestObjects = 1
		defer func() { maxManifestObjects = old }()
		var got map[string]string
		err := withExportSnapshot(ctx, ExportSource{Pool: dev}, func(tx pgx.Tx) error {
			got = readObjectContentTypes(ctx, tx, schema)
			return nil
		})
		// S3 lists "Z" < "a" < "b" (bytes); a heap-order read kept "b".
		if want := map[string]string{"Z": "x/Z", "a": "x/a"}; err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("content types = %v, err = %v; want %v", got, err, want)
		}
	})

	t.Run("a table locked by a schema change: reported as such", func(t *testing.T) {
		old := pgDumpLockWait
		pgDumpLockWait = 200 * time.Millisecond
		defer func() { pgDumpLockWait = old }()
		lock, err := dev.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(ctx) //nolint:errcheck
		if _, err := lock.Exec(ctx, `SET LOCAL ROLE eurobase_migrator; LOCK TABLE `+s+`.users IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		var se SectionExport
		_ = withExportSnapshot(ctx, ExportSource{Pool: dev}, func(tx pgx.Tx) error {
			se = exportSchemaDump(ctx, tx, ExportSource{Pool: dev}, zw, schema)
			return nil
		})
		if se.Status != TableFailed || !strings.Contains(se.Error, "held a table lock") {
			t.Errorf("section = %+v, want the lock reason", se)
		}
	})

	t.Run("pg_dump unavailable: reported, export incomplete", func(t *testing.T) {
		res, entries, meta := runTenantExport(t, gw, ExportSource{Pool: dev, PgDump: "/nonexistent/pg_dump"}, schema, projectID)
		if res.Complete {
			t.Error("export without a schema reported complete")
		}
		if sec := findSection(meta.Sections, "schema"); sec == nil || sec.Status != TableFailed {
			t.Errorf("schema section = %+v", sec)
		}
		if _, ok := entries[schemaDumpFile]; ok {
			t.Error("schema.sql written although pg_dump failed")
		}
		if !strings.Contains(strings.Join(res.Warnings, "\n"), "schema/schema.sql") {
			t.Errorf("warnings = %q", res.Warnings)
		}
	})

	t.Run("a dump that can't read the tables fails instead of shipping partial DDL", func(t *testing.T) {
		// The gateway role can't lock <schema>_ddl-owned tables.
		_, entries, meta := runTenantExport(t, gw, ExportSource{Pool: gw}, schema, projectID)
		if sec := findSection(meta.Sections, "schema"); sec == nil || sec.Status != TableFailed {
			t.Errorf("schema section = %+v", sec)
		}
		if _, ok := entries[schemaDumpFile]; ok {
			t.Error("partial schema.sql written")
		}
	})
}

func findSection(ss []SectionExport, name string) *SectionExport {
	for i := range ss {
		if ss[i].Section == name {
			return &ss[i]
		}
	}
	return nil
}

func findSeq(ss []SequenceValue, name string) *SequenceValue {
	for i := range ss {
		if ss[i].Sequence == name {
			return &ss[i]
		}
	}
	return nil
}
