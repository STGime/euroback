package compliance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExportFormatVersion is written to _metadata.json as format_version so a
// restore (#657) can tell which layout it reads. Version 1 (no field) had
// tables/ and the platform sections only; version 2 adds schema/ (#656).
const ExportFormatVersion = 2

// Files of the schema/ section of a tenant export (#656).
const (
	schemaDumpFile      = "schema/schema.sql"
	sequencesFile       = "schema/sequences.json"
	authManifestFile    = "schema/auth_manifest.json"
	storageManifestFile = "schema/storage_manifest.json"
)

// SectionExport records what a tenant export wrote for one non-table
// section (schema dump, sequence values, auth and storage manifests), in
// _metadata.json's sections list. Status is one of the Table* statuses.
type SectionExport struct {
	Section string `json:"section"`
	File    string `json:"file,omitempty"`
	Status  string `json:"status"`
	Count   *int   `json:"count,omitempty"`
	Error   string `json:"error,omitempty"`
}

// sectionWarnings says, for each section not exported in full, what's
// missing — the warning text shown with an incomplete export.
func sectionWarnings(sections []SectionExport) []string {
	var out []string
	for _, s := range sections {
		switch s.Status {
		case TableFailed:
			out = append(out, fmt.Sprintf("%s could not be exported: %s", sectionLabels[s.Section], s.Error))
		case TableTruncated:
			out = append(out, fmt.Sprintf("%s is truncated: %s", sectionLabels[s.Section], s.Error))
		}
	}
	return out
}

var sectionLabels = map[string]string{
	"schema":           "the schema (schema/schema.sql)",
	"sequences":        "sequence values (schema/sequences.json)",
	"auth_manifest":    "the auth manifest (schema/auth_manifest.json)",
	"storage_manifest": "the storage manifest (schema/storage_manifest.json)",
}

// ── schema/schema.sql ────────────────────────────────────────────────

// safeSchemaName is the shape of tenant schema names; anything else is
// refused before it reaches pg_dump's --schema pattern.
var safeSchemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// maxSchemaDumpBytes caps the schema dump held in memory before it is
// written to the zip (a failed dump must not leave a partial entry).
const maxSchemaDumpBytes = 64 << 20

// pgDumpTimeout bounds one schema dump.
var pgDumpTimeout = 5 * time.Minute

// exportSchemaDump writes schema/schema.sql: `pg_dump --schema-only` of
// the tenant schema, run by the platform as the export source's login
// (tenants get no catalog access). It imports tx's snapshot, so the DDL
// matches the rows in tables/ exactly. tx must be the export snapshot's
// top-level transaction: pg_export_snapshot refuses a subtransaction.
//
// Only the tenant's own schema is dumped (--strict-names), without owners
// or grants: those are the platform's roles, recreated by provisioning.
// Failures are recorded in the section, never fatal to the export; the
// pg_dump error output is logged, not stored — it names hosts.
func exportSchemaDump(ctx context.Context, tx pgx.Tx, src ExportSource, zw *zip.Writer, schemaName string) SectionExport {
	se := SectionExport{Section: "schema"}
	fail := func(msg string) SectionExport {
		se.Status, se.Error = TableFailed, msg
		return se
	}
	if !safeSchemaName.MatchString(schemaName) {
		return fail("unexpected schema name")
	}
	var snapshot string
	if err := tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&snapshot); err != nil {
		slog.Warn("export: pg_export_snapshot failed", "error", err)
		return fail("could not share the export snapshot")
	}
	bin := src.PgDump
	if bin == "" {
		bin = "pg_dump"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		slog.Warn("export: pg_dump not available", "error", err)
		return fail("pg_dump is not available on the export worker")
	}
	args := []string{
		"--schema-only", "--no-owner", "--no-privileges", "--no-security-labels",
		"--no-tablespaces", "--no-publications", "--no-subscriptions",
		"--strict-names", "--schema=" + `"` + schemaName + `"`,
		"--snapshot=" + snapshot, "--lock-wait-timeout=30s",
	}
	if src.Role != "" {
		args = append(args, "--role="+src.Role)
	}
	dctx, cancel := context.WithTimeout(ctx, pgDumpTimeout)
	defer cancel()
	cmd := exec.CommandContext(dctx, path, args...)
	cmd.Env = libpqEnv(src.Pool.Config())
	var out cappedBuffer
	out.max = maxSchemaDumpBytes
	var stderr cappedBuffer
	stderr.max = 8 << 10
	cmd.Stdout, cmd.Stderr = &out, &stderr
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if out.overflow {
			return fail(fmt.Sprintf("the schema dump is larger than %d MiB", maxSchemaDumpBytes>>20))
		}
		slog.Warn("export: pg_dump failed", "error", err, "stderr", stderr.String())
		// --lock-wait-timeout is applied as statement_timeout on the LOCK
		// TABLE (pg_dump runs everything else with statement_timeout 0).
		if e := stderr.String(); strings.Contains(e, "due to statement timeout") || strings.Contains(e, "due to lock timeout") {
			return fail("a schema change held a table lock for more than 30 s; request the export again")
		}
		return fail("pg_dump failed (" + err.Error() + ")")
	}
	f, err := zw.Create(schemaDumpFile)
	if err == nil {
		_, err = f.Write(out.Bytes())
	}
	if err != nil {
		return fail("write: " + err.Error())
	}
	se.File, se.Status = schemaDumpFile, TableExported
	return se
}

// libpqEnv is the environment pg_dump connects with: the same server,
// database, login and TLS settings as cfg (the export source pool), and
// nothing else from the worker's environment besides PATH.
func libpqEnv(cfg *pgxpool.Config) []string {
	cc := cfg.ConnConfig
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"PGHOST=" + cc.Host,
		"PGPORT=" + strconv.Itoa(int(cc.Port)),
		"PGUSER=" + cc.User,
		"PGDATABASE=" + cc.Database,
		"PGAPPNAME=eurobase-export-schema",
		"PGCONNECT_TIMEOUT=10",
		"PGPASSFILE=/dev/null",
	}
	if cc.Password != "" {
		env = append(env, "PGPASSWORD="+cc.Password)
	}
	// TLS: the connection string's settings, else the worker's PG* env
	// (pgx reads both); libpq's defaults match pgx's otherwise (prefer).
	params := connStringParams(cfg.ConnString())
	for _, k := range []string{"sslmode", "sslrootcert", "sslcert", "sslkey"} {
		v := params[k]
		if v == "" {
			v = os.Getenv("PG" + strings.ToUpper(k))
		}
		if v != "" {
			env = append(env, "PG"+strings.ToUpper(k)+"="+v)
		}
	}
	return env
}

// connStringParams returns the parameters of a postgres:// URL or a
// keyword=value connection string.
func connStringParams(s string) map[string]string {
	out := map[string]string{}
	if strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://") {
		u, err := url.Parse(s)
		if err != nil {
			return out
		}
		for k, v := range u.Query() {
			if len(v) > 0 {
				out[k] = v[0]
			}
		}
		return out
	}
	for _, field := range strings.Fields(s) {
		if k, v, ok := strings.Cut(field, "="); ok {
			out[k] = strings.Trim(v, `'`)
		}
	}
	return out
}

// cappedBuffer is a bytes.Buffer that fails writes past max.
type cappedBuffer struct {
	bytes.Buffer
	max      int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		b.overflow = true
		return 0, errors.New("output limit exceeded")
	}
	return b.Buffer.Write(p)
}

// ── schema/sequences.json ────────────────────────────────────────────

// SequenceValue is one entry of schema/sequences.json: what a restore
// passes to setval() after loading the rows.
type SequenceValue struct {
	Sequence  string `json:"sequence"`
	LastValue int64  `json:"last_value"`
	IsCalled  bool   `json:"is_called"`
}

// exportSequences writes the current value of every sequence in the
// tenant schema (identity and serial columns included) — pg_dump's
// --schema-only leaves them out.
func exportSequences(ctx context.Context, tx pgx.Tx, zw *zip.Writer, schemaName string) SectionExport {
	se := SectionExport{Section: "sequences"}
	sp, err := tx.Begin(ctx)
	if err != nil {
		se.Status, se.Error = TableFailed, errorText(err)
		return se
	}
	values, err := readSequences(ctx, sp, schemaName)
	if err != nil {
		_ = sp.Rollback(ctx)
		se.Status, se.Error = TableFailed, errorText(err)
		return se
	}
	_ = sp.Commit(ctx)
	if err := writeJSONToZip(zw, sequencesFile, values); err != nil {
		se.Status, se.Error = TableFailed, err.Error()
		return se
	}
	n := len(values)
	se.File, se.Status, se.Count = sequencesFile, TableExported, &n
	return se
}

func readSequences(ctx context.Context, tx pgx.Tx, schemaName string) ([]SequenceValue, error) {
	rows, err := tx.Query(ctx,
		`SELECT c.relname FROM pg_catalog.pg_class c
		   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = $1 AND c.relkind = 'S'
		  ORDER BY c.relname`, schemaName)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	values := make([]SequenceValue, 0, len(names))
	for _, name := range names {
		v := SequenceValue{Sequence: name}
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT last_value, is_called FROM %s.%s`,
			quoteIdent(schemaName), quoteIdent(name))).Scan(&v.LastValue, &v.IsCalled); err != nil {
			return nil, fmt.Errorf("sequence %s: %w", name, err)
		}
		values = append(values, v)
	}
	return values, nil
}

// ── schema/auth_manifest.json ────────────────────────────────────────

// AuthManifest is schema/auth_manifest.json: the project's auth settings
// and email templates. Secrets are never included — OAuth client secrets
// live in the vault (tables/vault_secrets.json lists their names, not
// their values), and any secret-looking value in the stored settings is
// dropped and listed in RedactedKeys.
type AuthManifest struct {
	AuthConfig     any                   `json:"auth_config"`
	RedactedKeys   []string              `json:"redacted_keys,omitempty"`
	EmailTemplates []EmailTemplateExport `json:"email_templates"`
	Note           string                `json:"note"`
}

// EmailTemplateExport is one customised auth email template.
type EmailTemplateExport struct {
	TemplateType string    `json:"template_type"`
	Subject      string    `json:"subject"`
	BodyHTML     string    `json:"body_html"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// secretKey matches settings keys whose non-boolean values are dropped
// from the auth manifest. Booleans (e.g. secret_set) are kept: they say
// whether something is configured, not what it is.
// Matched on the key's last _-separated part(s), so rate-limit knobs like
// token_refresh_per_5min_per_ip stay.
var secretKey = regexp.MustCompile(`(?i)(^|_)(secret|password|private_key|api_?key|token|credentials?)$`)

func exportAuthManifest(ctx context.Context, pool *pgxpool.Pool, zw *zip.Writer, projectID string) SectionExport {
	se := SectionExport{Section: "auth_manifest"}
	m, err := buildAuthManifest(ctx, pool, projectID)
	if err == nil {
		err = writeJSONToZip(zw, authManifestFile, m)
	}
	if err != nil {
		slog.Warn("export: auth manifest", "error", err, "project_id", projectID)
		se.Status, se.Error = TableFailed, errorText(err)
		return se
	}
	se.File, se.Status = authManifestFile, TableExported
	return se
}

func buildAuthManifest(ctx context.Context, pool *pgxpool.Pool, projectID string) (*AuthManifest, error) {
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT auth_config FROM public.projects WHERE id = $1::uuid`, projectID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("read auth settings: %w", err)
	}
	m := &AuthManifest{
		EmailTemplates: []EmailTemplateExport{},
		Note:           "Secrets are not exported: OAuth client secrets are kept in the project vault; re-enter them after a restore.",
	}
	if len(raw) > 0 {
		var cfg any
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse auth settings: %w", err)
		}
		m.AuthConfig = redactSecrets(cfg, "", &m.RedactedKeys)
		sort.Strings(m.RedactedKeys)
	}
	rows, err := pool.Query(ctx,
		`SELECT template_type, subject, body_html, updated_at FROM public.email_templates
		  WHERE project_id = $1::uuid ORDER BY template_type`, projectID)
	if err != nil {
		return nil, fmt.Errorf("read email templates: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t EmailTemplateExport
		if err := rows.Scan(&t.TemplateType, &t.Subject, &t.BodyHTML, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("read email templates: %w", err)
		}
		m.EmailTemplates = append(m.EmailTemplates, t)
	}
	return m, rows.Err()
}

// redactSecrets returns v without the non-boolean values of secret-looking
// keys, recording each dropped key's path.
func redactSecrets(v any, path string, redacted *[]string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if _, isBool := val.(bool); secretKey.MatchString(k) && !isBool && val != nil {
				*redacted = append(*redacted, p)
				continue
			}
			out[k] = redactSecrets(val, p, redacted)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactSecrets(val, fmt.Sprintf("%s[%d]", path, i), redacted)
		}
		return out
	default:
		return v
	}
}

// ── schema/storage_manifest.json ─────────────────────────────────────

// ObjectLister lists a bucket (*storage.S3Client).
type ObjectLister interface {
	ListObjects(ctx context.Context, bucketName, prefix string, limit int, continuationToken string) (*storage.ListResult, error)
}

// TenantExportOptions are the non-database inputs of a tenant export.
type TenantExportOptions struct {
	// Objects and Bucket list the project's storage for the storage
	// manifest. Without them the manifest is reported as not exported.
	Objects ObjectLister
	Bucket  string
}

// StorageManifest is schema/storage_manifest.json. Object bytes are not in
// the archive; the manifest says what a complete copy of storage holds.
type StorageManifest struct {
	Objects   []StorageManifestEntry `json:"objects"`
	Truncated bool                   `json:"truncated,omitempty"`
	// TrackingUnavailable: storage_objects couldn't be read, so no entry
	// has a content type and tracked says nothing.
	TrackingUnavailable bool   `json:"tracking_unavailable,omitempty"`
	Note                string `json:"note"`
}

// StorageManifestEntry is one stored object. ETag is the storage
// service's checksum: the MD5 of the object for a single-part upload,
// an MD5 of the part MD5s ("…-<parts>") for a multipart one.
// ContentType comes from the object's tracking row (tables/
// storage_objects.json); Tracked is false for objects without one.
type StorageManifestEntry struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag,omitempty"`
	LastModified time.Time `json:"last_modified"`
	ContentType  string    `json:"content_type,omitempty"`
	Tracked      bool      `json:"tracked"`
}

// maxManifestObjects caps the storage manifest.
var maxManifestObjects = 100000

// readObjectContentTypes maps tracked object keys to their content type,
// from the tenant's storage_objects table, inside the export snapshot.
// Rows are read in byte order (COLLATE "C"), the order S3 lists keys in,
// so past the cap the rows kept are the ones for the objects the manifest
// lists. nil when the table can't be read: the manifest then says tracking
// is unavailable rather than calling every object untracked.
func readObjectContentTypes(ctx context.Context, tx pgx.Tx, schemaName string) map[string]string {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil
	}
	defer sp.Rollback(ctx) //nolint:errcheck
	rows, err := sp.Query(ctx, fmt.Sprintf(`SELECT key, coalesce(content_type, '') FROM %s.storage_objects ORDER BY key COLLATE "C" LIMIT %d`,
		quoteIdent(schemaName), maxManifestObjects+1))
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, ct string
		if err := rows.Scan(&k, &ct); err != nil {
			return nil
		}
		out[k] = ct
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

func exportStorageManifest(ctx context.Context, zw *zip.Writer, opts TenantExportOptions, projectID string, contentTypes map[string]string) SectionExport {
	se := SectionExport{Section: "storage_manifest"}
	if opts.Objects == nil || opts.Bucket == "" {
		se.Status, se.Error = TableFailed, "storage is not configured on the export worker"
		return se
	}
	m := StorageManifest{
		Objects:             []StorageManifestEntry{},
		TrackingUnavailable: contentTypes == nil,
		Note:                "Object contents are not included in this archive; download them through the storage API.",
	}
	token := ""
	for {
		page, err := opts.Objects.ListObjects(ctx, opts.Bucket, "", 1000, token)
		if err != nil {
			slog.Warn("export: storage manifest", "error", err, "project_id", projectID)
			se.Status, se.Error = TableFailed, "listing the project's storage failed"
			return se
		}
		for _, o := range page.Objects {
			// Earlier export archives are the platform's, not the project's data.
			if storage.IsExportArchiveKey(projectID, o.Key) {
				continue
			}
			if len(m.Objects) == maxManifestObjects {
				m.Truncated = true
				break
			}
			ct, tracked := contentTypes[o.Key]
			m.Objects = append(m.Objects, StorageManifestEntry{
				Key: o.Key, Size: o.Size, ETag: o.ETag, LastModified: o.LastModified,
				ContentType: ct, Tracked: tracked,
			})
		}
		if m.Truncated || !page.IsTruncated || page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	if err := writeJSONToZip(zw, storageManifestFile, m); err != nil {
		se.Status, se.Error = TableFailed, err.Error()
		return se
	}
	n := len(m.Objects)
	se.File, se.Count = storageManifestFile, &n
	se.Status = TableExported
	if m.Truncated {
		se.Status, se.Error = TableTruncated, fmt.Sprintf("only the first %d objects are listed", maxManifestObjects)
	}
	return se
}
