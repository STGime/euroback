package compliance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5"
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
// schema_name is resolved from projects in one round-trip; we use a
// single QueryRow with EXISTS so the call is cheap (~1ms on hot path).
// quoteIdent guards the schema name even though it comes from a
// platform-controlled column, on the principle that defence in depth
// for identifier interpolation is free.
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
	if err := s.Pool.QueryRow(ctx, q, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check user existence: %w", err)
	}
	return exists, nil
}

// GetExportRequest retrieves an export request by ID, scoped to a project.
func (s *ExportService) GetExportRequest(ctx context.Context, exportID, projectID string) (*ExportRequest, error) {
	var req ExportRequest
	err := s.Pool.QueryRow(ctx,
		`SELECT id, project_id, user_id, status, format, s3_key, file_size, error,
		        requested_by, requested_by_type, started_at, completed_at, expires_at, created_at
		 FROM export_requests
		 WHERE id = $1 AND project_id = $2`,
		exportID, projectID,
	).Scan(&req.ID, &req.ProjectID, &req.UserID, &req.Status, &req.Format,
		&req.S3Key, &req.FileSize, &req.Error,
		&req.RequestedBy, &req.RequestedByType, &req.StartedAt, &req.CompletedAt, &req.ExpiresAt, &req.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get export request: %w", err)
	}
	return &req, nil
}

// GetExportRequestForUser retrieves an export by ID, scoped to a specific end-user.
func (s *ExportService) GetExportRequestForUser(ctx context.Context, exportID, projectID, userID string) (*ExportRequest, error) {
	var req ExportRequest
	err := s.Pool.QueryRow(ctx,
		`SELECT id, project_id, user_id, status, format, s3_key, file_size, error,
		        requested_by, requested_by_type, started_at, completed_at, expires_at, created_at
		 FROM export_requests
		 WHERE id = $1 AND project_id = $2 AND user_id = $3`,
		exportID, projectID, userID,
	).Scan(&req.ID, &req.ProjectID, &req.UserID, &req.Status, &req.Format,
		&req.S3Key, &req.FileSize, &req.Error,
		&req.RequestedBy, &req.RequestedByType, &req.StartedAt, &req.CompletedAt, &req.ExpiresAt, &req.CreatedAt)
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
		`SELECT id, project_id, user_id, status, format, s3_key, file_size, error,
		        requested_by, requested_by_type, started_at, completed_at, expires_at, created_at
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
		if err := rows.Scan(&req.ID, &req.ProjectID, &req.UserID, &req.Status, &req.Format,
			&req.S3Key, &req.FileSize, &req.Error,
			&req.RequestedBy, &req.RequestedByType, &req.StartedAt, &req.CompletedAt, &req.ExpiresAt, &req.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan export: %w", err)
		}
		results = append(results, req)
	}
	return results, nil
}

// CheckRateLimit returns true if the rate limit for this export type is exceeded.
// Tenant: 1 per project per hour. User: 1 per user per 24 hours.
func (s *ExportService) CheckRateLimit(ctx context.Context, projectID string, userID *string) (bool, error) {
	var count int
	if userID != nil {
		err := s.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM export_requests
			 WHERE project_id = $1 AND user_id = $2 AND created_at > now() - interval '24 hours'`,
			projectID, *userID,
		).Scan(&count)
		if err != nil {
			return false, err
		}
	} else {
		err := s.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM export_requests
			 WHERE project_id = $1 AND user_id IS NULL AND created_at > now() - interval '1 hour'`,
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

// MarkCompleted updates the export to completed state with S3 key and size.
func (s *ExportService) MarkCompleted(ctx context.Context, exportID, s3Key string, fileSize int64) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE export_requests
		 SET status = 'completed', s3_key = $2, file_size = $3,
		     completed_at = now(), expires_at = now() + interval '7 days'
		 WHERE id = $1`,
		exportID, s3Key, fileSize,
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
func (s *ExportService) GenerateDownloadURL(ctx context.Context, bucket, s3Key string) (string, error) {
	return s.S3.GeneratePresignedDownloadURL(ctx, bucket, s3Key, 1*time.Hour)
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

// TableRef describes a table that references users.
type TableRef struct {
	TableName  string
	UserColumn string
}

// DiscoverUserTables finds all tables in the tenant schema that have a user_id-like
// column (either named user_id or with a FK to users.id).
func DiscoverUserTables(ctx context.Context, pool *pgxpool.Pool, schemaName string) ([]TableRef, error) {
	rows, err := pool.Query(ctx,
		`SELECT DISTINCT c.table_name, c.column_name
		 FROM information_schema.columns c
		 WHERE c.table_schema = $1
		   AND c.table_name != 'users'
		   AND (
		       c.column_name = 'user_id'
		       OR EXISTS (
		           SELECT 1 FROM information_schema.key_column_usage kcu
		           JOIN information_schema.referential_constraints rc
		               ON kcu.constraint_name = rc.constraint_name
		               AND kcu.constraint_schema = rc.constraint_schema
		           JOIN information_schema.key_column_usage rcu
		               ON rc.unique_constraint_name = rcu.constraint_name
		               AND rc.unique_constraint_schema = rcu.constraint_schema
		           WHERE kcu.table_schema = $1
		             AND kcu.table_name = c.table_name
		             AND kcu.column_name = c.column_name
		             AND rcu.table_schema = $1
		             AND rcu.table_name = 'users'
		             AND rcu.column_name = 'id'
		       )
		   )
		 ORDER BY c.table_name`,
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

// ListTenantTables returns all user-created tables in a tenant schema (excludes system tables).
func ListTenantTables(ctx context.Context, pool *pgxpool.Pool, schemaName string) ([]string, error) {
	rows, err := pool.Query(ctx,
		`SELECT table_name
		 FROM information_schema.tables
		 WHERE table_schema = $1
		   AND table_type = 'BASE TABLE'
		 ORDER BY table_name`,
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
	ExportID    string    `json:"export_id"`
	ProjectID   string    `json:"project_id"`
	UserID      *string   `json:"user_id,omitempty"`
	Format      string    `json:"format"`
	ExportedAt  time.Time `json:"exported_at"`
	TableCount  int       `json:"table_count"`
	TotalRows   int       `json:"total_rows"`
}

// BuildTenantExportZip generates a zip archive of all tenant data.
func BuildTenantExportZip(ctx context.Context, pool *pgxpool.Pool, schemaName, projectID, exportID, format string) (*bytes.Buffer, int, error) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	totalRows := 0

	tables, err := ListTenantTables(ctx, pool, schemaName)
	if err != nil {
		return nil, 0, err
	}

	for _, table := range tables {
		rows, err := queryTable(ctx, pool, schemaName, table, "", "")
		if err != nil {
			slog.Warn("export: failed to query table", "table", table, "error", err)
			continue
		}
		n, err := writeTableToZip(zw, fmt.Sprintf("tables/%s", table), rows, format)
		if err != nil {
			slog.Warn("export: failed to write table", "table", table, "error", err)
			continue
		}
		totalRows += n
	}

	// Audit log for this project
	auditData, err := queryRaw(ctx, pool,
		`SELECT id, actor_id, actor_email, action, target_type, target_id, metadata, ip_address, created_at
		 FROM public.audit_log WHERE project_id = $1 ORDER BY created_at DESC LIMIT 10000`,
		projectID,
	)
	if err == nil && len(auditData) > 0 {
		n, _ := writeTableToZip(zw, "_audit_log", auditData, format)
		totalRows += n
	}

	meta := ExportMetadata{
		ExportID:   exportID,
		ProjectID:  projectID,
		Format:     format,
		ExportedAt: time.Now().UTC(),
		TableCount: len(tables),
		TotalRows:  totalRows,
	}
	writeJSONToZip(zw, "_metadata.json", meta)

	if err := zw.Close(); err != nil {
		return nil, 0, fmt.Errorf("close zip: %w", err)
	}
	return buf, totalRows, nil
}

// BuildUserExportZip generates a zip archive of a specific user's data.
func BuildUserExportZip(ctx context.Context, pool *pgxpool.Pool, schemaName, projectID, userID, exportID, format string) (*bytes.Buffer, int, error) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	totalRows := 0

	// User profile
	profileData, err := queryTable(ctx, pool, schemaName, "users", "id", userID)
	if err == nil && len(profileData) > 0 {
		n, _ := writeTableToZip(zw, "_user_profile", profileData, format)
		totalRows += n
	}

	// Discover tables with user_id references
	refs, err := DiscoverUserTables(ctx, pool, schemaName)
	if err != nil {
		return nil, 0, err
	}

	for _, ref := range refs {
		rows, err := queryTable(ctx, pool, schemaName, ref.TableName, ref.UserColumn, userID)
		if err != nil {
			slog.Warn("export: failed to query user table", "table", ref.TableName, "error", err)
			continue
		}
		n, err := writeTableToZip(zw, fmt.Sprintf("tables/%s", ref.TableName), rows, format)
		if err != nil {
			continue
		}
		totalRows += n
	}

	// User's audit entries
	auditData, err := queryRaw(ctx, pool,
		`SELECT id, actor_id, actor_email, action, target_type, target_id, metadata, ip_address, created_at
		 FROM public.audit_log
		 WHERE project_id = $1 AND (actor_id = $2 OR target_id = $2)
		 ORDER BY created_at DESC LIMIT 5000`,
		projectID, userID,
	)
	if err == nil && len(auditData) > 0 {
		n, _ := writeTableToZip(zw, "_audit_log", auditData, format)
		totalRows += n
	}

	meta := ExportMetadata{
		ExportID:   exportID,
		ProjectID:  projectID,
		UserID:     &userID,
		Format:     format,
		ExportedAt: time.Now().UTC(),
		TableCount: len(refs) + 1,
		TotalRows:  totalRows,
	}
	writeJSONToZip(zw, "_metadata.json", meta)

	if err := zw.Close(); err != nil {
		return nil, 0, fmt.Errorf("close zip: %w", err)
	}
	return buf, totalRows, nil
}

// ── Internal helpers ───────────────────────────────────────────────────────

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func queryTable(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, filterCol, filterVal string) ([]map[string]interface{}, error) {
	q := fmt.Sprintf(`SELECT * FROM %s.%s`, quoteIdent(schemaName), quoteIdent(tableName))
	var args []interface{}
	if filterCol != "" && filterVal != "" {
		q += fmt.Sprintf(` WHERE %s = $1`, quoteIdent(filterCol))
		args = append(args, filterVal)
	}
	q += " LIMIT 100000"

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	descs := rows.FieldDescriptions()
	var results []map[string]interface{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]interface{}, len(descs))
		for i, desc := range descs {
			row[string(desc.Name)] = vals[i]
		}
		results = append(results, row)
	}
	return results, nil
}

func writeTableToZip(zw *zip.Writer, name string, rows []map[string]interface{}, format string) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	if format == "csv" {
		return writeCSVToZip(zw, name+".csv", rows)
	}
	return writeJSONRowsToZip(zw, name+".json", rows)
}

// queryRaw executes a SQL query and returns the results as generic maps.
func queryRaw(ctx context.Context, pool *pgxpool.Pool, sql string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	descs := rows.FieldDescriptions()
	var results []map[string]interface{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]interface{}, len(descs))
		for i, desc := range descs {
			row[string(desc.Name)] = vals[i]
		}
		results = append(results, row)
	}
	return results, nil
}

func writeJSONToZip(zw *zip.Writer, name string, data interface{}) {
	w, err := zw.Create(name)
	if err != nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(data)
}

func writeJSONRowsToZip(zw *zip.Writer, name string, rows []map[string]interface{}) (int, error) {
	w, err := zw.Create(name)
	if err != nil {
		return 0, err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func writeCSVToZip(zw *zip.Writer, name string, rows []map[string]interface{}) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	w, err := zw.Create(name)
	if err != nil {
		return 0, err
	}
	return writeCSV(w, rows)
}

func writeCSV(w io.Writer, rows []map[string]interface{}) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	// Collect column names from first row (stable order).
	var cols []string
	for k := range rows[0] {
		cols = append(cols, k)
	}

	cw := csv.NewWriter(w)
	_ = cw.Write(cols)

	for _, row := range rows {
		record := make([]string, len(cols))
		for i, col := range cols {
			record[i] = formatCSVCell(row[col])
		}
		_ = cw.Write(record)
	}
	cw.Flush()
	return len(rows), cw.Error()
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
