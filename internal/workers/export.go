package workers

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

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
	if err := exportSvc.MarkRunning(ctx, args.ExportID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	schemaName, s3Bucket, err := resolveProject(ctx, w.DBPool, args.ProjectID)
	if err != nil {
		_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		return err
	}

	logger.Info("building tenant export zip")
	buf, totalRows, err := compliance.BuildTenantExportZip(ctx, w.DBPool, schemaName, args.ProjectID, args.ExportID, args.Format)
	if err != nil {
		_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		return fmt.Errorf("build zip: %w", err)
	}

	s3Key := fmt.Sprintf("exports/%s/%s.zip", args.ProjectID, args.ExportID)
	logger.Info("uploading export to s3", "key", s3Key, "size", buf.Len(), "rows", totalRows)

	if err := w.S3.UploadObject(ctx, s3Bucket, s3Key, bytes.NewReader(buf.Bytes()), "application/zip", int64(buf.Len())); err != nil {
		_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		return fmt.Errorf("upload: %w", err)
	}

	if err := exportSvc.MarkCompleted(ctx, args.ExportID, s3Key, int64(buf.Len())); err != nil {
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
				"file_size": buf.Len(),
				"rows":      totalRows,
				"scope":     "tenant",
			}))
	}

	logger.Info("tenant export completed", "size", buf.Len(), "rows", totalRows)
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
	if err := exportSvc.MarkRunning(ctx, args.ExportID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	schemaName, s3Bucket, err := resolveProject(ctx, w.DBPool, args.ProjectID)
	if err != nil {
		_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		return err
	}

	logger.Info("building user export zip")
	buf, totalRows, err := compliance.BuildUserExportZip(ctx, w.DBPool, schemaName, args.ProjectID, args.UserID, args.ExportID, args.Format)
	if err != nil {
		_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		return fmt.Errorf("build zip: %w", err)
	}

	s3Key := fmt.Sprintf("exports/%s/users/%s/%s.zip", args.ProjectID, args.UserID, args.ExportID)
	logger.Info("uploading user export to s3", "key", s3Key, "size", buf.Len(), "rows", totalRows)

	if err := w.S3.UploadObject(ctx, s3Bucket, s3Key, bytes.NewReader(buf.Bytes()), "application/zip", int64(buf.Len())); err != nil {
		_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		return fmt.Errorf("upload: %w", err)
	}

	if err := exportSvc.MarkCompleted(ctx, args.ExportID, s3Key, int64(buf.Len())); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	// Closes #100, per-user / self-serve variant.
	if w.AuditSvc != nil {
		w.AuditSvc.Log(ctx, args.ProjectID, "", "",
			audit.ActionExportCompleted,
			audit.WithTarget("export", args.ExportID),
			audit.WithMetadata(map[string]any{
				"s3_key":         s3Key,
				"file_size":      buf.Len(),
				"rows":           totalRows,
				"scope":          "user",
				"target_user_id": args.UserID,
			}))
	}

	logger.Info("user export completed", "size", buf.Len(), "rows", totalRows)
	return nil
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
