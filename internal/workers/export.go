package workers

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// TenantExportWorker handles async full-tenant DSAR exports.
type TenantExportWorker struct {
	river.WorkerDefaults[jobs.TenantExportArgs]
	DBPool   *pgxpool.Pool
	S3       *storage.S3Client
	AuditSvc *audit.Service
}

func (w *TenantExportWorker) Work(ctx context.Context, job *river.Job[jobs.TenantExportArgs]) error {
	args := job.Args
	logger := slog.With("export_id", args.ExportID, "project_id", args.ProjectID, "type", "tenant")

	exportSvc := compliance.NewExportService(w.DBPool, w.S3, w.AuditSvc)
	failExport := func(stage string, err error) {
		_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		// Closes #100 (failed-paths follow-up). Workers run async so
		// the requester's HTTP response is already returned by the
		// time we get here; the audit row is the only place a failure
		// surfaces in the Compliance feed. Actor fields are empty —
		// the request-time audit row records the human; this entry
		// records the system event.
		if w.AuditSvc != nil {
			w.AuditSvc.Log(ctx, args.ProjectID, "", "",
				audit.ActionExportFailed,
				audit.WithTarget("export", args.ExportID),
				audit.WithMetadata(map[string]any{
					"scope": "tenant",
					"stage": stage,
					"error": err.Error(),
				}))
		}
	}

	if err := exportSvc.MarkRunning(ctx, args.ExportID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	schemaName, s3Bucket, err := resolveProject(ctx, w.DBPool, args.ProjectID)
	if err != nil {
		failExport("resolve_project", err)
		return err
	}

	logger.Info("streaming tenant export zip to temp file")
	tmpFile, size, totalRows, err := streamExportToTempFile(
		ctx, args.ExportID,
		func(out io.Writer) (int, error) {
			return compliance.WriteTenantExport(ctx, w.DBPool, out, schemaName, args.ProjectID, args.ExportID, args.Format)
		},
	)
	if err != nil {
		failExport("build_zip", err)
		return fmt.Errorf("build zip: %w", err)
	}
	defer cleanupTempFile(tmpFile)

	s3Key := fmt.Sprintf("exports/%s/%s.zip", args.ProjectID, args.ExportID)
	logger.Info("uploading export to s3", "key", s3Key, "size", size, "rows", totalRows)

	if err := w.S3.UploadObject(ctx, s3Bucket, s3Key, tmpFile, "application/zip", size); err != nil {
		failExport("upload", err)
		return fmt.Errorf("upload: %w", err)
	}

	if err := exportSvc.MarkCompleted(ctx, args.ExportID, s3Key, size); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	// Closes #100. The request-time audit row was written by the
	// HTTP handler; here we close the loop with the completion
	// event. Workers have no http.Request so we pass an empty
	// actor (the request audit already records who initiated it)
	// and put the matching export_id in target_id so the two rows
	// link cleanly.
	if w.AuditSvc != nil {
		w.AuditSvc.Log(ctx, args.ProjectID, "", "",
			audit.ActionExportCompleted,
			audit.WithTarget("export", args.ExportID),
			audit.WithMetadata(map[string]any{
				"s3_key":    s3Key,
				"file_size": size,
				"rows":      totalRows,
				"scope":     "tenant",
			}))
	}

	logger.Info("tenant export completed", "size", size, "rows", totalRows)
	return nil
}

// UserExportWorker handles async per-user DSAR exports.
type UserExportWorker struct {
	river.WorkerDefaults[jobs.UserExportArgs]
	DBPool   *pgxpool.Pool
	S3       *storage.S3Client
	AuditSvc *audit.Service
}

func (w *UserExportWorker) Work(ctx context.Context, job *river.Job[jobs.UserExportArgs]) error {
	args := job.Args
	logger := slog.With("export_id", args.ExportID, "project_id", args.ProjectID, "user_id", args.UserID, "type", "user")

	exportSvc := compliance.NewExportService(w.DBPool, w.S3, w.AuditSvc)
	failExport := func(stage string, err error) {
		_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		if w.AuditSvc != nil {
			w.AuditSvc.Log(ctx, args.ProjectID, "", "",
				audit.ActionExportFailed,
				audit.WithTarget("export", args.ExportID),
				audit.WithMetadata(map[string]any{
					"scope":          "user",
					"target_user_id": args.UserID,
					"stage":          stage,
					"error":          err.Error(),
				}))
		}
	}

	if err := exportSvc.MarkRunning(ctx, args.ExportID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	schemaName, s3Bucket, err := resolveProject(ctx, w.DBPool, args.ProjectID)
	if err != nil {
		failExport("resolve_project", err)
		return err
	}

	logger.Info("streaming user export zip to temp file")
	tmpFile, size, totalRows, err := streamExportToTempFile(
		ctx, args.ExportID,
		func(out io.Writer) (int, error) {
			return compliance.WriteUserExport(ctx, w.DBPool, out, schemaName, args.ProjectID, args.UserID, args.ExportID, args.Format)
		},
	)
	if err != nil {
		failExport("build_zip", err)
		return fmt.Errorf("build zip: %w", err)
	}
	defer cleanupTempFile(tmpFile)

	s3Key := fmt.Sprintf("exports/%s/users/%s/%s.zip", args.ProjectID, args.UserID, args.ExportID)
	logger.Info("uploading user export to s3", "key", s3Key, "size", size, "rows", totalRows)

	if err := w.S3.UploadObject(ctx, s3Bucket, s3Key, tmpFile, "application/zip", size); err != nil {
		failExport("upload", err)
		return fmt.Errorf("upload: %w", err)
	}

	if err := exportSvc.MarkCompleted(ctx, args.ExportID, s3Key, size); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	// Closes #100, per-user / self-serve variant.
	if w.AuditSvc != nil {
		w.AuditSvc.Log(ctx, args.ProjectID, "", "",
			audit.ActionExportCompleted,
			audit.WithTarget("export", args.ExportID),
			audit.WithMetadata(map[string]any{
				"s3_key":         s3Key,
				"file_size":      size,
				"rows":           totalRows,
				"scope":          "user",
				"target_user_id": args.UserID,
			}))
	}

	logger.Info("user export completed", "size", size, "rows", totalRows)
	return nil
}

// streamExportToTempFile drives one of the compliance.Write*Export
// streaming functions into a temp file on local disk, then rewinds it
// for the upload step. Closes #99: the previous Build*ExportZip
// returned a *bytes.Buffer holding the entire archive in RAM. For a
// tenant with millions of rows that buffer alone could OOM the worker
// pod (each row was also kept as a []map[string]interface{} before
// being zipped). The temp file gives us a bounded-memory pipeline:
// row → CSV/JSON encoder → zip compressor → disk → S3.
//
// The file lives in the OS temp dir (Linux /tmp, K8s emptyDir by
// default), is removed on exit even on the error path, and the
// uploaded size comes from Seek(end) rather than buf.Len() — so the
// size given to S3 always matches the bytes actually streamed.
func streamExportToTempFile(_ context.Context, exportID string, write func(io.Writer) (int, error)) (*os.File, int64, int, error) {
	f, err := os.CreateTemp("", "dsar-export-"+exportID+"-*.zip")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("create temp file: %w", err)
	}
	totalRows, err := write(f)
	if err != nil {
		cleanupTempFile(f)
		return nil, 0, 0, err
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		cleanupTempFile(f)
		return nil, 0, 0, fmt.Errorf("seek temp file: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		cleanupTempFile(f)
		return nil, 0, 0, fmt.Errorf("rewind temp file: %w", err)
	}
	return f, size, totalRows, nil
}

func cleanupTempFile(f *os.File) {
	if f == nil {
		return
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove export temp file", "path", name, "error", err)
	}
}

func resolveProject(ctx context.Context, pool *pgxpool.Pool, projectID string) (schemaName, s3Bucket string, err error) {
	err = pool.QueryRow(ctx,
		`SELECT schema_name, s3_bucket FROM projects WHERE id = $1`,
		projectID,
	).Scan(&schemaName, &s3Bucket)
	if err != nil {
		return "", "", fmt.Errorf("resolve project %s: %w", projectID, err)
	}
	return schemaName, s3Bucket, nil
}
