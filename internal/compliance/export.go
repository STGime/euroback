package compliance

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	edb "github.com/eurobase/euroback/internal/db"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// ExportRequest tracks a pending, running, or completed DSAR export.
type ExportRequest struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	UserID          *string    `json:"user_id,omitempty"`
	Status          string     `json:"status"`
	Format          string     `json:"format"`
	S3Key           *string    `json:"s3_key,omitempty"`
	FileSize        *int64     `json:"file_size,omitempty"`
	Error           *string    `json:"error,omitempty"`
	RequestedBy     string     `json:"requested_by"`
	RequestedByType string     `json:"requested_by_type"`
	DownloadURL     string     `json:"download_url,omitempty"`
	Complete        *bool      `json:"complete,omitempty"` // false: archive incomplete, see Warnings (#654); nil before it was recorded
	Warnings        []string   `json:"warnings,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ExportService handles DSAR export request lifecycle.
type ExportService struct {
	Pool        *pgxpool.Pool
	S3          *storage.S3Client
	AuditSvc    *audit.Service
	riverClient *river.Client[pgx.Tx]
}

// NewExportService creates a new ExportService with an insert-only River client.
func NewExportService(pool *pgxpool.Pool, s3 *storage.S3Client, auditSvc *audit.Service) *ExportService {
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		slog.Error("failed to create river client for export service", "error", err)
	}
	return &ExportService{Pool: pool, S3: s3, AuditSvc: auditSvc, riverClient: rc}
}

// EnqueueTenantExport inserts a River job for a full-tenant export.
func (s *ExportService) EnqueueTenantExport(ctx context.Context, exportID, projectID, format string) error {
	if s.riverClient == nil {
		return fmt.Errorf("river client not available")
	}
	_, err := s.riverClient.Insert(ctx, jobs.TenantExportArgs{
		ExportID:  exportID,
		ProjectID: projectID,
		Format:    format,
	}, nil)
	return err
}

// EnqueueUserExport inserts a River job for a per-user export.
func (s *ExportService) EnqueueUserExport(ctx context.Context, exportID, projectID, userID, format string) error {
	if s.riverClient == nil {
		return fmt.Errorf("river client not available")
	}
	_, err := s.riverClient.Insert(ctx, jobs.UserExportArgs{
		ExportID:  exportID,
		ProjectID: projectID,
		UserID:    userID,
		Format:    format,
	}, nil)
	return err
}

// CreateExportRequest inserts a new export_requests row and returns it.
func (s *ExportService) CreateExportRequest(ctx context.Context, projectID string, userID *string, format, requestedBy, requestedByType string) (*ExportRequest, error) {
	var req ExportRequest
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO export_requests (project_id, user_id, format, requested_by, requested_by_type)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, project_id, user_id, status, format, requested_by, requested_by_type, created_at`,
		projectID, userID, format, requestedBy, requestedByType,
	).Scan(&req.ID, &req.ProjectID, &req.UserID, &req.Status, &req.Format, &req.RequestedBy, &req.RequestedByType, &req.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert export request: %w", err)
	}
	return &req, nil
}

// UserExistsInTenant reports whether the given user_id exists in the
// tenant's `users` table. Closes #102: HandleRequestUserExport used
// to accept any UUID and enqueue a worker even if the user wasn't in
// the tenant — producing a useless near-empty zip and burning compute.
//
// schema_name is resolved from projects in one round-trip; the EXISTS
// query against the tenant's users table runs through edb.RunAsService
// so app.end_user_role='service' is set for the transaction. Without
// that GUC, RLS on `<schema>.users` filters every row out (no
// authenticated end-user context exists here — the actor is the
// platform admin), and EXISTS would falsely return false even for a
// user that's clearly present. listEndUsers already uses the same
// pattern. quoteIdent guards the schema identifier even though it
// comes from a platform-controlled column, as defence-in-depth.
func (s *ExportService) UserExistsInTenant(ctx context.Context, projectID, userID string) (bool, error) {
	var schemaName string
	err := s.Pool.QueryRow(ctx,
		`SELECT schema_name FROM projects WHERE id = $1`, projectID,
	).Scan(&schemaName)
	if err != nil {
		return false, fmt.Errorf("resolve project schema: %w", err)
	}

	var exists bool
	q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s.users WHERE id = $1)`, quoteIdent(schemaName))
	if err := edb.RunAsService(ctx, s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, userID).Scan(&exists)
	}); err != nil {
		return false, fmt.Errorf("check user existence: %w", err)
	}
	return exists, nil
}

const exportRequestColumns = `id, project_id, user_id, status, format, s3_key, file_size, error,
	requested_by, requested_by_type, complete, warnings, started_at, completed_at, expires_at, created_at`

func scanExportRequest(row pgx.Row, req *ExportRequest) error {
	return row.Scan(&req.ID, &req.ProjectID, &req.UserID, &req.Status, &req.Format,
		&req.S3Key, &req.FileSize, &req.Error,
		&req.RequestedBy, &req.RequestedByType, &req.Complete, &req.Warnings,
		&req.StartedAt, &req.CompletedAt, &req.ExpiresAt, &req.CreatedAt)
}

// GetExportRequest retrieves an export request by ID, scoped to a project.
func (s *ExportService) GetExportRequest(ctx context.Context, exportID, projectID string) (*ExportRequest, error) {
	var req ExportRequest
	err := scanExportRequest(s.Pool.QueryRow(ctx,
		`SELECT `+exportRequestColumns+`
		 FROM export_requests
		 WHERE id = $1 AND project_id = $2`,
		exportID, projectID,
	), &req)
	if err != nil {
		return nil, fmt.Errorf("get export request: %w", err)
	}
	return &req, nil
}

// GetExportRequestForUser retrieves an export by ID, scoped to a specific end-user.
func (s *ExportService) GetExportRequestForUser(ctx context.Context, exportID, projectID, userID string) (*ExportRequest, error) {
	var req ExportRequest
	err := scanExportRequest(s.Pool.QueryRow(ctx,
		`SELECT `+exportRequestColumns+`
		 FROM export_requests
		 WHERE id = $1 AND project_id = $2 AND user_id = $3`,
		exportID, projectID, userID,
	), &req)
	if err != nil {
		return nil, fmt.Errorf("get export request for user: %w", err)
	}
	return &req, nil
}

// ListExports returns paginated export history for a project.
func (s *ExportService) ListExports(ctx context.Context, projectID string, limit, offset int) ([]ExportRequest, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT `+exportRequestColumns+`
		 FROM export_requests
		 WHERE project_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2 OFFSET $3`,
		projectID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list exports: %w", err)
	}
	defer rows.Close()

	var results []ExportRequest
	for rows.Next() {
		var req ExportRequest
		if err := scanExportRequest(rows, &req); err != nil {
			return nil, fmt.Errorf("scan export: %w", err)
		}
		results = append(results, req)
	}
	return results, rows.Err()
}

// CheckRateLimit returns true if the rate limit for this export type is exceeded.
// Tenant: 1 per project per hour. User: 1 per user per 24 hours.
//
// Closes #101. The previous query counted every row, including ones
// that ended in `status = 'failed'`. That punished users for our
// failures: if a worker crashed (bad schema, S3 outage, OOM), the
// retry was blocked for 24h. The fix is one predicate — only
// in-flight (pending/running) and completed exports count toward
// the budget. A failed export is effectively a no-op and the user
// can re-request immediately.
func (s *ExportService) CheckRateLimit(ctx context.Context, projectID string, userID *string) (bool, error) {
	var count int
	if userID != nil {
		err := s.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM export_requests
			 WHERE project_id = $1 AND user_id = $2
			   AND status <> 'failed'
			   AND created_at > now() - interval '24 hours'`,
			projectID, *userID,
		).Scan(&count)
		if err != nil {
			return false, err
		}
	} else {
		err := s.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM export_requests
			 WHERE project_id = $1 AND user_id IS NULL
			   AND status <> 'failed'
			   AND created_at > now() - interval '1 hour'`,
			projectID,
		).Scan(&count)
		if err != nil {
			return false, err
		}
	}
	return count > 0, nil
}

// MarkRunning updates the export to running state.
func (s *ExportService) MarkRunning(ctx context.Context, exportID string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE export_requests SET status = 'running', started_at = now() WHERE id = $1`,
		exportID,
	)
	return err
}

// MarkCompleted updates the export to completed state with S3 key and
// size, and records whether the archive is complete (#654).
func (s *ExportService) MarkCompleted(ctx context.Context, exportID, s3Key string, fileSize int64, res *ExportResult) error {
	warnings := []string{}
	complete := false
	if res != nil {
		complete = res.Complete
		if res.Warnings != nil {
			warnings = res.Warnings
		}
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE export_requests
		 SET status = 'completed', s3_key = $2, file_size = $3,
		     complete = $4, warnings = $5,
		     completed_at = now(), expires_at = now() + interval '7 days'
		 WHERE id = $1`,
		exportID, s3Key, fileSize, complete, warnings,
	)
	return err
}

// MarkFailed updates the export to failed state with an error message.
func (s *ExportService) MarkFailed(ctx context.Context, exportID, errMsg string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE export_requests SET status = 'failed', error = $2, completed_at = now() WHERE id = $1`,
		exportID, errMsg,
	)
	return err
}

// GenerateDownloadURL creates a presigned S3 URL for a completed export.
// The URL carries a `Content-Disposition: attachment; filename=…` directive
// so the browser saves the zip with a recognisable name (project slug +
// scope + date) instead of the export UUID. Falls back to the S3 key's
// basename if the project lookup fails — the URL still works, only the
// suggested filename is generic.
func (s *ExportService) GenerateDownloadURL(ctx context.Context, bucket string, req *ExportRequest) (string, error) {
	if req == nil || req.S3Key == nil || *req.S3Key == "" {
		return "", fmt.Errorf("export request has no s3 key")
	}
	filename := s.buildExportFilename(ctx, req)
	return s.S3.GeneratePresignedDownloadURLAs(ctx, bucket, *req.S3Key, 1*time.Hour, filename)
}

// buildExportFilename derives a human-readable filename for a DSAR
// download. Format:
//
//	eurobase-<slug>-<scope>-<YYYY-MM-DD>.zip
//
// where <scope> is `project` for a tenant export or `user-<short>` for
// a per-user export. The slug comes from public.projects; if the
// lookup fails we use the project_id's first 8 chars as a fallback so
// the file still has something distinctive in the name.
func (s *ExportService) buildExportFilename(ctx context.Context, req *ExportRequest) string {
	slug := s.lookupProjectSlug(ctx, req.ProjectID)
	if slug == "" {
		slug = shortID(req.ProjectID)
	}

	scope := "project"
	if req.UserID != nil && *req.UserID != "" {
		scope = "user-" + shortID(*req.UserID)
	}

	date := req.CreatedAt.UTC().Format("2006-01-02")
	return fmt.Sprintf("eurobase-%s-%s-%s.zip", sanitiseFilenamePart(slug), scope, date)
}

// lookupProjectSlug fetches the slug for a project. Returns "" on any
// error — the caller falls back to the project_id prefix. Best-effort:
// the download URL itself still works regardless.
func (s *ExportService) lookupProjectSlug(ctx context.Context, projectID string) string {
	var slug string
	err := s.Pool.QueryRow(ctx,
		`SELECT slug FROM projects WHERE id = $1`, projectID,
	).Scan(&slug)
	if err != nil {
		return ""
	}
	return slug
}

// shortID returns the first 8 characters of a UUID-shaped id, or the
// full string if it's shorter. Used as a stable, recognisable handle
// in the filename when we can't or don't want to leak the full UUID.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// sanitiseFilenamePart strips characters that are awkward in a
// filename or in a `Content-Disposition` header. Quotes, slashes,
// backslashes, and control chars go. Whitespace collapses to '-'.
// We're already starting from a slug, but defence-in-depth.
func sanitiseFilenamePart(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '"' || r == '\\' || r == '/' || r < 0x20 || r == 0x7f:
			// drop
		case r == ' ' || r == '\t':
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "project"
	}
	return out
}

// CleanupExpired deletes expired export records and their S3 objects.
func (s *ExportService) CleanupExpired(ctx context.Context) {
	rows, err := s.Pool.Query(ctx,
		`DELETE FROM export_requests WHERE expires_at < now() RETURNING s3_key, project_id`)
	if err != nil {
		slog.Error("failed to cleanup expired exports", "error", err)
		return
	}
	defer rows.Close()

	var deleted int
	for rows.Next() {
		var s3Key *string
		var projectID string
		if err := rows.Scan(&s3Key, &projectID); err != nil {
			continue
		}
		if s3Key != nil && *s3Key != "" {
			bucket := resolveBucket(ctx, s.Pool, projectID)
			if bucket != "" {
				if err := s.S3.DeleteObject(ctx, bucket, *s3Key); err != nil {
					slog.Warn("failed to delete expired export from s3", "key", *s3Key, "error", err)
				}
			}
		}
		deleted++
	}
	if deleted > 0 {
		slog.Info("cleaned up expired exports", "count", deleted)
	}
}

func resolveBucket(ctx context.Context, pool *pgxpool.Pool, projectID string) string {
	var bucket string
	_ = pool.QueryRow(ctx, `SELECT s3_bucket FROM projects WHERE id = $1`, projectID).Scan(&bucket)
	return bucket
}

// ── Export data generation ─────────────────────────────────────────────────

// ExportSource is where an export reads tenant table data from (#654).
//
// Tenant tables carry RLS policies written for end users. Read through a
// runtime role, they return only the rows those policies allow — often
// none — and the export used to report success with those tables left
// out. The export therefore reads as a role that owns, or inherits the
// ownership of, every table in the tenant schema: in production the
// worker's developer pool as eurobase_developer itself, with no Role.
// It owns tables legacy MCP DDL created and, via INHERIT, has the
// privileges of eurobase_migrator (platform and SQL-editor tables),
// `<schema>_ddl` (tenant migrations) and eurobase_gateway (legacy SDK
// DDL). SET ROLE eurobase_migrator would lose the developer-owned ones.
//
// The read also runs with row_security = off, so a table where a policy
// would still apply (FORCE ROW LEVEL SECURITY, an owner outside that set)
// makes Postgres raise an error instead of returning fewer rows. Such a
// table is reported as failed — never exported as empty.
type ExportSource struct {
	Pool *pgxpool.Pool
	// Role, if set, is assumed with SET LOCAL ROLE for the read
	// transaction.
	Role string
}

// querier is satisfied by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// withExportSnapshot runs fn in a read-only REPEATABLE READ transaction
// on src, so every table in one export comes from the same snapshot.
func withExportSnapshot(ctx context.Context, src ExportSource, fn func(pgx.Tx) error) error {
	tx, err := src.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin export snapshot: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if src.Role != "" {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+quoteIdent(src.Role)); err != nil {
			return fmt.Errorf("export: set role %s: %w", src.Role, err)
		}
	}
	// Refuse, rather than silently filter, any read RLS would still limit.
	if _, err := tx.Exec(ctx, "SET LOCAL row_security = off"); err != nil {
		return fmt.Errorf("export: row_security: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '5min'"); err != nil {
		return fmt.Errorf("export: statement_timeout: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// maxRowsPerTable caps each table in an export. A larger table is
// exported up to the cap and reported as truncated.
var maxRowsPerTable = 100000

// Table export statuses in _metadata.json.
const (
	TableExported  = "exported"
	TableTruncated = "truncated"
	TableFailed    = "failed"
)

// TableExport records what an export wrote for one table.
type TableExport struct {
	Table  string `json:"table"`
	File   string `json:"file,omitempty"` // absent when nothing was written
	Rows   int    `json:"rows"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// RedactedColumns were left out on purpose: credential columns of
	// the platform-managed tables (query.SensitiveSystemColumns).
	RedactedColumns []string `json:"redacted_columns,omitempty"`
}

// ExportResult is the outcome of building an export archive.
type ExportResult struct {
	TotalRows int
	// Complete is false when any table failed or was truncated, or a
	// section (audit log, retention holds) could not be read.
	Complete bool
	Warnings []string
}

// exportTable streams one table (or a filtered query over it) into the
// zip inside its own savepoint, so a failing table doesn't abort the
// snapshot for the tables after it.
func exportTable(ctx context.Context, tx pgx.Tx, zw *zip.Writer, table, file, sql string, args []any, format string) TableExport {
	te := TableExport{Table: table}
	sp, err := tx.Begin(ctx)
	if err != nil {
		te.Status, te.Error = TableFailed, err.Error()
		return te
	}
	// One row past the cap tells a truncated table from one of exactly
	// maxRowsPerTable rows, without reading the rest of a large table.
	sql = fmt.Sprintf("%s LIMIT %d", sql, maxRowsPerTable+1)
	res, err := streamQueryToZip(ctx, sp, zw, sql, args, file, format,
		streamOpts{writeEmpty: true, limit: maxRowsPerTable, omit: query.SensitiveSystemColumns(table)})
	te.Rows = res.Rows
	te.RedactedColumns = res.Omitted
	if res.Written {
		te.File = res.File
	}
	switch {
	case err != nil:
		_ = sp.Rollback(ctx)
		te.Status, te.Error = TableFailed, errorText(err)
	case res.Truncated:
		_ = sp.Commit(ctx)
		te.Status = TableTruncated
	default:
		if err := sp.Commit(ctx); err != nil {
			te.Status, te.Error = TableFailed, errorText(err)
		} else {
			te.Status = TableExported
		}
	}
	return te
}

// errorText is the message recorded for a failed table: the server's
// message for database errors (e.g. `query would be affected by
// row-level security policy for table "x"`), the Go error otherwise.
func errorText(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	return err.Error()
}

// tableWarnings turns non-exported tables into warning lines.
func tableWarnings(tables []TableExport) []string {
	var out []string
	for _, t := range tables {
		switch t.Status {
		case TableFailed:
			out = append(out, fmt.Sprintf("table %q was not exported: %s", t.Table, t.Error))
		case TableTruncated:
			out = append(out, fmt.Sprintf("table %q was truncated at %d rows", t.Table, t.Rows))
		}
	}
	return out
}

// zipFileNames maps table names to safe, unique zip entry names under
// dir. Names made of [A-Za-z0-9_.-] are kept as they are; anything else
// (a quoted identifier with "/" or "..", say) is replaced with "_", so no
// entry can land outside dir when the archive is extracted. Uniqueness is
// case-insensitive, for case-insensitive filesystems.
func zipFileNames(dir string, tables []string) map[string]string {
	out := make(map[string]string, len(tables))
	used := map[string]bool{}
	for _, t := range tables {
		var b strings.Builder
		for _, r := range t {
			if r == '_' || r == '-' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		base := b.String()
		if base == "" || strings.HasPrefix(base, ".") {
			base = "_" + base
		}
		name := base
		for i := 2; used[strings.ToLower(name)]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		used[strings.ToLower(name)] = true
		out[t] = dir + "/" + name
	}
	return out
}

// TableRef describes a table that references users.
type TableRef struct {
	TableName  string
	UserColumn string
}

// DiscoverUserTables finds all tables in the tenant schema that have a
// user_id-like column (either named user_id or a single-column FK to
// users.id). It reads pg_catalog, not information_schema, so tables the
// caller has no privilege on are still found (information_schema would
// hide them, and the export would silently skip them — #654). A column
// named user_id counts only if its type can hold the subject's id (uuid
// or text-like, domains included): an integer user_id is some other id,
// and comparing it to a UUID would fail the table in every DSAR.
func DiscoverUserTables(ctx context.Context, q querier, schemaName string) ([]TableRef, error) {
	rows, err := q.Query(ctx,
		`SELECT DISTINCT c.relname, a.attname
		 FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		 WHERE n.nspname = $1
		   AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		   AND c.relname <> 'users'
		   AND (
		       (a.attname = 'user_id'
		        AND (SELECT CASE WHEN t.typtype = 'd' THEN t.typbasetype ELSE t.oid END
		             FROM pg_type t WHERE t.oid = a.atttypid)
		            IN ('uuid'::regtype, 'text'::regtype, 'character varying'::regtype))
		       OR EXISTS (
		           SELECT 1
		           FROM pg_constraint k
		           JOIN pg_class rc ON rc.oid = k.confrelid
		           JOIN pg_attribute ra ON ra.attrelid = rc.oid AND ra.attnum = k.confkey[1]
		           WHERE k.contype = 'f'
		             AND k.conrelid = c.oid
		             AND cardinality(k.conkey) = 1
		             AND k.conkey[1] = a.attnum
		             AND rc.relnamespace = n.oid
		             AND rc.relname = 'users'
		             AND ra.attname = 'id'
		       )
		   )
		 ORDER BY 1, 2`,
		schemaName,
	)
	if err != nil {
		return nil, fmt.Errorf("discover user tables: %w", err)
	}
	defer rows.Close()

	var refs []TableRef
	for rows.Next() {
		var ref TableRef
		if err := rows.Scan(&ref.TableName, &ref.UserColumn); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// ListTenantTables returns every table in a tenant schema (partitioned
// tables once, not their partitions). It reads pg_catalog rather than
// information_schema, which only lists tables the caller has a privilege
// on — so a table the export can't read shows up as failed instead of
// silently disappearing (#654).
func ListTenantTables(ctx context.Context, q querier, schemaName string) ([]string, error) {
	rows, err := q.Query(ctx,
		`SELECT c.relname
		 FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1
		   AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		 ORDER BY c.relname`,
		schemaName,
	)
	if err != nil {
		return nil, fmt.Errorf("list tenant tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, nil
}

// ExportMetadata is the _metadata.json file in the export zip.
type ExportMetadata struct {
	ExportID   string    `json:"export_id"`
	ProjectID  string    `json:"project_id"`
	UserID     *string   `json:"user_id,omitempty"`
	Format     string    `json:"format"`
	ExportedAt time.Time `json:"exported_at"`
	TableCount int       `json:"table_count"`
	TotalRows  int       `json:"total_rows"`
	// Complete is true only when every table was exported in full and
	// every section could be read. Warnings says what is missing (#654).
	Complete bool     `json:"complete"`
	Warnings []string `json:"warnings,omitempty"`
	// Tables lists every table the export covered, with its file, row
	// count and status (exported | truncated | failed).
	Tables           []TableExport `json:"tables"`
	RowLimitPerTable int           `json:"row_limit_per_table"`
	// AuditLogUnavailable: the audit-log section could not be read.
	AuditLogUnavailable bool `json:"audit_log_unavailable,omitempty"`
	// RetentionHoldCount is the number of active
	// retention-hold rows recorded against this project (tenant
	// exports) or scoped to this requester (user exports — see
	// filterUserRetentionHolds). Only present when the collection
	// query succeeded — if RetentionHoldsUnavailable is true, this
	// field is omitted so the archive does not affirmatively assert
	// "no holds" while the underlying query failed.
	RetentionHoldCount *int `json:"retention_hold_count,omitempty"`
	// RetentionHoldsUnavailable signals that the retention-hold
	// query failed during export. The primary export still ships;
	// this flag tells the recipient / regulator that the count is
	// unknown rather than zero — a misstatement in a legal
	// disclosure is worse than an honest "unavailable."
	RetentionHoldsUnavailable bool `json:"retention_holds_unavailable,omitempty"`
}

// RetentionHoldExportEntry is the shape of each row in
// `_retention_holds.json` inside a DSAR export archive. Named apart
// from the internal `RetentionHold` struct because this is a public
// contract with recipients (regulators, DPOs, end-users) and should
// stay stable across internal renames.
//
// TargetRefRedacted is set on per-user exports when a row/object hold
// concerns some *other* subject: we still disclose that a hold exists
// (legal_basis + target_type + expires_at) but omit the identifier so
// the requester does not receive personal data about another subject
// (GDPR Art. 15(4)).
type RetentionHoldExportEntry struct {
	LegalBasis        string    `json:"legal_basis"`
	ExpiresAt         time.Time `json:"expires_at"`
	TargetType        string    `json:"target_type"` // "row" | "object" | "table"
	TargetRef         any       `json:"target_ref,omitempty"`
	TargetRefRedacted bool      `json:"target_ref_redacted,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// filterUserRetentionHolds narrows a project-wide hold list to what
// is disclosable to a single subject in an Art. 15 access request.
//
//   - "table" holds are kept verbatim — they name a schema+table, not
//     a subject, so no cross-subject data is leaked.
//   - "row" / "object" holds have their target_ref inspected for the
//     requester's userID. If the ref plausibly points at this user
//     (userID appears anywhere in the serialised ref — e.g. as a pkey
//     value or embedded in an object key like "users/<uid>/…"), the
//     ref is preserved. Otherwise target_ref is dropped and
//     target_ref_redacted is set to true, so the requester still
//     learns "there are N additional retained items" but not *whose*.
//
// Assumption: refs are single-subject. The shapes we produce today
// ({schema,table,pkey:{user_id:X}} for rows, users/<uid>/… for
// objects) name one subject. A future two-party shape (e.g. messages
// pkey {"from":A,"to":B} or matter files naming client + counterparty)
// would ride along under this rule — the whole ref is disclosed once
// A's id appears in it, so B's id would leak too. If/when that shape
// ships, switch to field-level redaction here. The other direction
// (a surrogate-key ref belonging to A but not containing A's UUID) is
// already the safe one — redacted rather than leaked.
//
// Art. 15(4): "the right to obtain a copy … shall not adversely
// affect the rights and freedoms of others." Handing subject A a
// hold naming subject B's row primary key is such an adverse effect;
// this filter closes that gap without under-disclosing the fact of
// retention.
func filterUserRetentionHolds(all []RetentionHoldExportEntry, userID string) []RetentionHoldExportEntry {
	out := make([]RetentionHoldExportEntry, 0, len(all))
	for _, h := range all {
		if h.TargetType == "table" {
			out = append(out, h)
			continue
		}
		// row / object — inspect the serialised ref for userID.
		refBytes, err := json.Marshal(h.TargetRef)
		if err == nil && userID != "" && strings.Contains(string(refBytes), userID) {
			out = append(out, h)
			continue
		}
		out = append(out, RetentionHoldExportEntry{
			LegalBasis:        h.LegalBasis,
			ExpiresAt:         h.ExpiresAt,
			TargetType:        h.TargetType,
			TargetRef:         nil,
			TargetRefRedacted: true,
			CreatedAt:         h.CreatedAt,
		})
	}
	return out
}

// collectRetentionHolds returns the export-shaped view of every
// active hold for the project. WriteTenantExport uses the list
// as-is; WriteUserExport runs it through filterUserRetentionHolds
// to redact refs that would leak other subjects' identifiers.
func collectRetentionHolds(ctx context.Context, pool *pgxpool.Pool, projectID string) ([]RetentionHoldExportEntry, error) {
	rows, err := pool.Query(ctx,
		`SELECT legal_basis, expires_at, target_type, target_ref, created_at
		   FROM public.retention_holds
		  WHERE project_id = $1::uuid
		    AND expires_at > now()
		  ORDER BY expires_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query retention_holds: %w", err)
	}
	defer rows.Close()

	out := make([]RetentionHoldExportEntry, 0)
	for rows.Next() {
		var e RetentionHoldExportEntry
		var raw []byte
		if err := rows.Scan(&e.LegalBasis, &e.ExpiresAt, &e.TargetType, &raw, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan retention_holds: %w", err)
		}
		// target_ref is JSONB. Unmarshal into `any` so writeJSONToZip
		// re-emits it as native JSON rather than a base64 blob.
		var ref any
		if err := json.Unmarshal(raw, &ref); err != nil {
			return nil, fmt.Errorf("unmarshal target_ref: %w", err)
		}
		e.TargetRef = ref
		out = append(out, e)
	}
	return out, rows.Err()
}

// WriteTenantExport streams a tenant-scope export zip into w. Closes
// #99: the previous BuildTenantExportZip materialised the entire archive
// in a *bytes.Buffer and accumulated every row of every table into a
// []map[string]interface{} before zipping — a single tenant with one
// large table could OOM the worker pod. The streaming variant iterates
// rows.Next() row-by-row through the zip writer, so memory is bounded
// by one row + zip compression buffer regardless of table size.
//
// Callers pass a temp file or io.Pipe so the zip never lands in RAM.
//
// Tenant tables are read from src (see ExportSource); platform sections
// (audit log, retention holds) from pool. Every table gets a file —
// empty tables an empty one — and an entry in _metadata.json's tables
// list; anything not exported in full makes the result incomplete, with
// a warning saying why (#654).
func WriteTenantExport(ctx context.Context, pool *pgxpool.Pool, src ExportSource, w io.Writer, schemaName, projectID, exportID, format string) (*ExportResult, error) {
	zw := zip.NewWriter(w)
	res := &ExportResult{}

	var tables []string
	var exported []TableExport
	err := withExportSnapshot(ctx, src, func(tx pgx.Tx) error {
		var err error
		if tables, err = ListTenantTables(ctx, tx, schemaName); err != nil {
			return err
		}
		files := zipFileNames("tables", tables)
		for _, table := range tables {
			q := fmt.Sprintf(`SELECT * FROM %s.%s`, quoteIdent(schemaName), quoteIdent(table))
			te := exportTable(ctx, tx, zw, table, files[table], q, nil, format)
			if te.Status == TableFailed {
				slog.Warn("export: table not exported", "table", table, "error", te.Error, "project_id", projectID)
			}
			res.TotalRows += te.Rows
			exported = append(exported, te)
		}
		return nil
	})
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	res.Warnings = tableWarnings(exported)

	// Audit log for this project. The 10k cap is a soft bound — a
	// streaming archive could in principle take more, but at 10k
	// rows the zip is already representative of "what we audited"
	// and beyond that the recipient is better served by a targeted
	// query.
	auditRes, err := streamQueryToZip(ctx, pool, zw,
		`SELECT id, actor_id, actor_email, action, target_type, target_id, metadata, ip_address, created_at
		 FROM public.audit_log WHERE project_id = $1 ORDER BY created_at DESC LIMIT 10000`,
		[]interface{}{projectID},
		"_audit_log", format, streamOpts{},
	)
	auditUnavailable := err != nil
	if auditUnavailable {
		slog.Warn("export: audit log not exported", "error", err, "project_id", projectID)
		res.Warnings = append(res.Warnings, "the audit log could not be exported")
	} else {
		res.TotalRows += auditRes.Rows
	}

	// Retention holds — Legal-Team-tier disclosure (issue #314).
	// A tenant may hold items under §50 BRAO / §257 HGB / §147 AO
	// (or arbitrary legal basis); the DSAR recipient needs to see
	// them so they can reconcile "here's your data" with "here's
	// what we're additionally retaining under legal obligation."
	// A failure to gather holds is warn-only — the primary export
	// still ships; but we surface the failure explicitly via
	// retention_holds_unavailable rather than lying with count=0.
	holds, herr := collectRetentionHolds(ctx, pool, projectID)
	if herr != nil {
		slog.Warn("export: collect retention holds failed", "error", herr, "project_id", projectID)
		res.Warnings = append(res.Warnings, "retention holds could not be read")
	}
	res.Complete = len(res.Warnings) == 0
	meta := ExportMetadata{
		ExportID:            exportID,
		ProjectID:           projectID,
		Format:              format,
		ExportedAt:          time.Now().UTC(),
		TableCount:          len(tables),
		TotalRows:           res.TotalRows,
		Complete:            res.Complete,
		Warnings:            res.Warnings,
		Tables:              exported,
		RowLimitPerTable:    maxRowsPerTable,
		AuditLogUnavailable: auditUnavailable,
	}
	if herr != nil {
		meta.RetentionHoldsUnavailable = true
	} else {
		writeJSONToZip(zw, "_retention_holds.json", holds)
		n := len(holds)
		meta.RetentionHoldCount = &n
	}
	if err := writeJSONToZip(zw, "_metadata.json", meta); err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("write _metadata.json: %w", err)
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return res, nil
}

// WriteUserExport streams a per-user export zip into w. Same streaming
// contract as WriteTenantExport — see #99 — and the same completeness
// reporting (#654): the subject's rows are read from src, so RLS written
// for end users can't hide them from a DSAR.
func WriteUserExport(ctx context.Context, pool *pgxpool.Pool, src ExportSource, w io.Writer, schemaName, projectID, userID, exportID, format string) (*ExportResult, error) {
	zw := zip.NewWriter(w)
	res := &ExportResult{}

	var refs []TableRef
	var exported []TableExport
	err := withExportSnapshot(ctx, src, func(tx pgx.Tx) error {
		// User profile.
		te := exportTable(ctx, tx, zw, "users", "_user_profile",
			fmt.Sprintf(`SELECT * FROM %s.users WHERE id = $1`, quoteIdent(schemaName)),
			[]any{userID}, format)
		res.TotalRows += te.Rows
		exported = append(exported, te)

		var err error
		if refs, err = DiscoverUserTables(ctx, tx, schemaName); err != nil {
			return err
		}
		names := make([]string, len(refs))
		for i, r := range refs {
			names[i] = r.TableName
		}
		files := zipFileNames("tables", names)
		for _, ref := range refs {
			q := fmt.Sprintf(`SELECT * FROM %s.%s WHERE %s = $1`,
				quoteIdent(schemaName), quoteIdent(ref.TableName), quoteIdent(ref.UserColumn))
			te := exportTable(ctx, tx, zw, ref.TableName, files[ref.TableName], q, []any{userID}, format)
			if te.Status == TableFailed {
				slog.Warn("export: user table not exported", "table", ref.TableName, "error", te.Error, "project_id", projectID)
			}
			res.TotalRows += te.Rows
			exported = append(exported, te)
		}
		return nil
	})
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	res.Warnings = tableWarnings(exported)

	// User's audit entries. actor_id is uuid and target_id text, so the id
	// is passed once per type — a single shared parameter can't be both,
	// and this query failed on every run until #654 surfaced the error.
	auditRes, err := streamQueryToZip(ctx, pool, zw,
		`SELECT id, actor_id, actor_email, action, target_type, target_id, metadata, ip_address, created_at
		 FROM public.audit_log
		 WHERE project_id = $1 AND (actor_id = $2::uuid OR target_id = $3::text)
		 ORDER BY created_at DESC LIMIT 5000`,
		[]interface{}{projectID, userID, userID},
		"_audit_log", format, streamOpts{},
	)
	auditUnavailable := err != nil
	if auditUnavailable {
		slog.Warn("export: user audit log not exported", "error", err, "project_id", projectID)
		res.Warnings = append(res.Warnings, "the audit log could not be exported")
	} else {
		res.TotalRows += auditRes.Rows
	}

	// Retention holds — see filterUserRetentionHolds. Table-scope
	// holds ship verbatim (schema/table names aren't personal data
	// about other subjects). Row/object holds are kept if their
	// target_ref plausibly references this user; otherwise
	// target_ref is redacted so the fact of retention is disclosed
	// (count + legal basis + expiry) without leaking another
	// subject's identifier. GDPR Art. 15(4) — a copy under Art. 15
	// must not adversely affect the rights and freedoms of others.
	// Warn-only on query failure: primary export still ships,
	// metadata carries retention_holds_unavailable rather than a
	// false count=0.
	allHolds, herr := collectRetentionHolds(ctx, pool, projectID)
	if herr != nil {
		slog.Warn("export: collect retention holds failed", "error", herr, "project_id", projectID, "user_id", userID)
		res.Warnings = append(res.Warnings, "retention holds could not be read")
	}
	res.Complete = len(res.Warnings) == 0
	meta := ExportMetadata{
		ExportID:            exportID,
		ProjectID:           projectID,
		UserID:              &userID,
		Format:              format,
		ExportedAt:          time.Now().UTC(),
		TableCount:          len(refs) + 1,
		TotalRows:           res.TotalRows,
		Complete:            res.Complete,
		Warnings:            res.Warnings,
		Tables:              exported,
		RowLimitPerTable:    maxRowsPerTable,
		AuditLogUnavailable: auditUnavailable,
	}
	if herr != nil {
		meta.RetentionHoldsUnavailable = true
	} else {
		holds := filterUserRetentionHolds(allHolds, userID)
		writeJSONToZip(zw, "_retention_holds.json", holds)
		n := len(holds)
		meta.RetentionHoldCount = &n
	}
	if err := writeJSONToZip(zw, "_metadata.json", meta); err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("write _metadata.json: %w", err)
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return res, nil
}

// ── Internal helpers ───────────────────────────────────────────────────────

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// streamOpts tunes streamQueryToZip.
type streamOpts struct {
	// writeEmpty creates the entry even when the query returns no rows,
	// so a missing file never means "empty" (#654).
	writeEmpty bool
	// limit, if > 0, stops after that many rows and reports Truncated.
	limit int
	// omit names columns to leave out of the output (redaction).
	omit map[string]bool
}

// streamResult is what streamQueryToZip wrote.
type streamResult struct {
	Rows      int
	Truncated bool
	Written   bool     // an entry was created in the zip
	File      string   // its name, when Written
	Omitted   []string // columns present in the result but left out (opts.omit)
}

// streamQueryToZip opens a zip entry, runs the query, and pipes rows
// through it without ever holding more than one row in Go memory at a
// time. The entry is created at the first row (or at the end, with
// writeEmpty), so a query that fails before returning anything leaves no
// file. If it fails part-way, the file written so far is closed as valid
// JSON / CSV and the error is returned — callers record the table as
// failed, with its row count.
//
// JSON output is an indented array — same shape as the old
// json.Encoder.Encode([]map) so existing consumers (the SDK test
// fixtures, the console download viewer, any scripts customers wrote
// against v0.4) don't see a format change. The cost of preserving the
// array shape over streaming JSON Lines is one extra byte per row plus
// some bookkeeping.
func streamQueryToZip(ctx context.Context, q querier, zw *zip.Writer, sql string, args []interface{}, zipPath, format string, o streamOpts) (streamResult, error) {
	var res streamResult
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	// keep[i] is the index into the row's values of output column i.
	var cols []string
	var keep []int
	for i, d := range rows.FieldDescriptions() {
		if o.omit[d.Name] {
			res.Omitted = append(res.Omitted, d.Name)
			continue
		}
		cols = append(cols, d.Name)
		keep = append(keep, i)
	}

	var entry io.Writer
	var cw *csv.Writer
	jsonFirst := true
	open := func() error {
		name := zipPath + ".json"
		if format == "csv" {
			name = zipPath + ".csv"
		}
		var err error
		if entry, err = zw.Create(name); err != nil {
			return err
		}
		res.Written, res.File = true, name
		if format == "csv" {
			cw = csv.NewWriter(entry)
			return cw.Write(cols)
		}
		_, err = io.WriteString(entry, "[\n")
		return err
	}
	// finish closes the entry so it is valid JSON / CSV, also after an
	// error part-way through.
	finish := func() error {
		if !res.Written {
			return nil
		}
		if cw != nil {
			cw.Flush()
			return cw.Error()
		}
		tail := "\n]\n"
		if jsonFirst {
			tail = "]\n"
		}
		_, err := io.WriteString(entry, tail)
		return err
	}
	fail := func(err error) (streamResult, error) {
		_ = finish()
		return res, err
	}

	for rows.Next() {
		if o.limit > 0 && res.Rows == o.limit {
			res.Truncated = true
			break
		}
		vals, err := rows.Values()
		if err != nil {
			return fail(err)
		}
		if !res.Written {
			if err := open(); err != nil {
				return fail(err)
			}
		}

		if format == "csv" {
			record := make([]string, len(cols))
			for i, vi := range keep {
				record[i] = formatCSVCell(exportValue(vals[vi]))
			}
			if err := cw.Write(record); err != nil {
				return fail(err)
			}
		} else {
			row := make(map[string]interface{}, len(cols))
			for i, c := range cols {
				row[c] = exportValue(vals[keep[i]])
			}
			if !jsonFirst {
				if _, err := io.WriteString(entry, ",\n"); err != nil {
					return fail(err)
				}
			}
			jsonFirst = false
			b, err := json.MarshalIndent(row, "  ", "  ")
			if err != nil {
				return fail(err)
			}
			if _, err := io.WriteString(entry, "  "); err != nil {
				return fail(err)
			}
			if _, err := entry.Write(b); err != nil {
				return fail(err)
			}
		}
		res.Rows++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	if !res.Written && o.writeEmpty {
		if err := open(); err != nil {
			return fail(err)
		}
	}
	return res, finish()
}

// exportValue converts driver values that don't serialise meaningfully:
// pgx returns uuid as [16]byte, which JSON renders as an array of 16
// numbers and CSV as "[72 247 …]" — every id in an export was unusable
// for a restore until #654. Arrays (uuid[]) are converted element-wise.
func exportValue(v any) any {
	switch val := v.(type) {
	case [16]byte:
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", val[0:4], val[4:6], val[6:8], val[8:10], val[10:16])
	case []any:
		out := make([]any, len(val))
		for i, e := range val {
			out[i] = exportValue(e)
		}
		return out
	default:
		return v
	}
}

func writeJSONToZip(zw *zip.Writer, name string, data interface{}) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// formatCSVCell renders one column value for the DSAR CSV export.
//
// Closes #103. The previous fmt.Sprintf("%v", …) path mangled jsonb
// and array columns into Go's map[k:v] / [a b c] string forms, which
// is neither valid JSON nor parseable back into anything useful. The
// DSAR recipient needed those columns intact to satisfy GDPR's
// "machine readable, structured format" obligation.
//
// Behaviour by type:
//   - nil               → empty string (CSV null)
//   - map / slice / any composite → json.Marshal so jsonb /
//                         text[] / row arrays round-trip
//   - time.Time          → RFC3339Nano (matches what pgx emits and
//                         what most spreadsheet apps recognise)
//   - []byte             → string conversion (Postgres bytea
//                         literal; if it happens to be UTF-8 text,
//                         the cell is human-readable, otherwise
//                         the recipient gets the raw bytes the
//                         pg driver returned)
//   - everything else    → fmt.Sprint, same as before
func formatCSVCell(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case time.Time:
		return v.Format(time.RFC3339Nano)
	case []byte:
		return string(v)
	case map[string]interface{}, []interface{}:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	default:
		return fmt.Sprint(v)
	}
}
