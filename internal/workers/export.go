package workers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// exportJobTimeout is the wall-clock budget of one export attempt
// (River's default is 1 minute, #370). It is also the upper bound on how
// long an export holds its REPEATABLE READ snapshot — and with it the
// vacuum horizon — on the shared cluster (#665): when it fires, the job
// context is canceled, the transaction rolls back and the snapshot is
// released. Both export kinds run at most MaxAttempts (2) times.
//
// Per attempt: a timed-out export is not retried (see exportAttemptFinal),
// since a timeout is almost always deterministic for a given tenant size.
const exportJobTimeout = 30 * time.Minute

// exportAttemptFinal reports whether this attempt's failure is the export's
// final outcome: the last attempt, or a timeout (not retried). Earlier
// failures leave the row "running" — River retries — so an end user
// polling the export doesn't see "failed" and then a completed export.
func exportAttemptFinal(jobCtx context.Context, attempt, maxAttempts int) bool {
	return attempt >= maxAttempts || errors.Is(jobCtx.Err(), context.DeadlineExceeded)
}

// exportResult turns a timed-out attempt's error into a River cancel, so it
// isn't retried (the snapshot would be held for another full timeout).
func exportResult(jobCtx context.Context, err error) error {
	if err != nil && errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
		return river.JobCancel(err)
	}
	return err
}

// exportSlowAfter: an export running longer than this is logged as a
// warning ("export slow") — the signal to add a budget or split exports
// before tenants grow into exportJobTimeout.
const exportSlowAfter = 5 * time.Minute

// exportBookkeepingContext is the context the export_requests bookkeeping
// and its audit rows run on (MarkFailed, and MarkCompleted once the archive
// is uploaded). When the job timeout fires, the job context is already
// canceled; bookkeeping on it would leave the row "running" forever — and,
// after a successful upload, an archive no row points at, which the expiry
// cleanup would never delete.
func exportBookkeepingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

// logExportDuration logs how long an export attempt took; slow ones as a
// warning.
func logExportDuration(logger *slog.Logger, started time.Time, outcome string) {
	d := time.Since(started)
	if d > exportSlowAfter {
		logger.Warn("export slow", "duration_seconds", int(d.Seconds()), "outcome", outcome)
		return
	}
	logger.Info("export duration", "duration_seconds", int(d.Seconds()), "outcome", outcome)
}

// TenantExportWorker handles async full-tenant DSAR exports.
type TenantExportWorker struct {
	river.WorkerDefaults[jobs.TenantExportArgs]
	DBPool   *pgxpool.Pool
	S3       *storage.S3Client
	AuditSvc *audit.Service
	// Data is where tenant tables are read from (compliance.ExportSource,
	// #654): the developer pool, as eurobase_developer, in production. Unset
	// = DBPool, where RLS-limited tables are reported as not exported.
	Data compliance.ExportSource
	// Dedicated opens the owner pool of a Team-tier project's dedicated
	// database, where its tenant data lives (#663). Unset = shared only.
	Dedicated DedicatedResolver
}

// Timeout overrides River's 1-minute default (#370).
func (w *TenantExportWorker) Timeout(*river.Job[jobs.TenantExportArgs]) time.Duration {
	return exportJobTimeout
}

func (w *TenantExportWorker) Work(ctx context.Context, job *river.Job[jobs.TenantExportArgs]) (err error) {
	args := job.Args
	logger := slog.With("export_id", args.ExportID, "project_id", args.ProjectID, "type", "tenant")
	started := time.Now()
	defer func() {
		outcome := "completed"
		if err != nil {
			outcome = "failed"
		}
		logExportDuration(logger, started, outcome)
		err = exportResult(ctx, err)
	}()

	exportSvc := compliance.NewExportService(w.DBPool, w.S3, w.AuditSvc)
	failExport := func(stage string, err error) {
		final := exportAttemptFinal(ctx, job.Attempt, job.MaxAttempts)
		ctx, cancel := exportBookkeepingContext(ctx)
		defer cancel()
		if final {
			_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		}
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
					"scope":      "tenant",
					"stage":      stage,
					"error":      err.Error(),
					"will_retry": !final,
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
	if err := refuseDuringUpgrade(ctx, upgradeStatePool(w.Data, w.DBPool), args.ProjectID); err != nil {
		failExport("upgrade_in_progress", err)
		return err
	}
	src, closeSrc, err := resolveExportSource(ctx, w.Data, w.DBPool, w.Dedicated, args.ProjectID)
	if err != nil {
		failExport("resolve_database", err)
		return err
	}
	defer closeSrc()

	// The storage manifest lists the project's bucket (#656). Never a
	// typed nil in the interface: that would look configured.
	opts := compliance.TenantExportOptions{Bucket: s3Bucket}
	if w.S3 != nil {
		opts.Objects = w.S3
	}

	logger.Info("streaming tenant export zip to temp file")
	var result *compliance.ExportResult
	tmpFile, size, totalRows, err := streamExportToTempFile(
		ctx, args.ExportID,
		func(out io.Writer) (int, error) {
			res, err := compliance.WriteTenantExport(ctx, w.DBPool, src, out, schemaName, args.ProjectID, args.ExportID, args.Format, opts)
			if err != nil {
				return 0, err
			}
			result = res
			return res.TotalRows, nil
		},
	)
	if err != nil {
		failExport("build_zip", err)
		return fmt.Errorf("build zip: %w", err)
	}
	defer cleanupTempFile(tmpFile)

	s3Key := storage.ExportArchivePrefix(args.ProjectID) + args.ExportID + ".zip"
	logger.Info("uploading export to s3", "key", s3Key, "size", size, "rows", totalRows)

	if err := w.S3.UploadObject(ctx, s3Bucket, s3Key, tmpFile, "application/zip", size); err != nil {
		failExport("upload", err)
		return fmt.Errorf("upload: %w", err)
	}

	// The archive is uploaded: record it even if the job context expires
	// now, or remove it — never leave an archive no row points at.
	bctx, bcancel := exportBookkeepingContext(ctx)
	defer bcancel()
	if err := exportSvc.MarkCompleted(bctx, args.ExportID, s3Key, size, result); err != nil {
		if derr := w.S3.DeleteObject(bctx, s3Bucket, s3Key); derr != nil {
			logger.Error("export archive uploaded but not recorded, and could not be deleted", "key", s3Key, "error", derr)
		}
		failExport("mark_completed", err)
		return fmt.Errorf("mark completed: %w", err)
	}
	if !result.Complete {
		logger.Warn("tenant export is incomplete", "warnings", result.Warnings)
	}

	// Closes #100. The request-time audit row was written by the
	// HTTP handler; here we close the loop with the completion
	// event. Workers have no http.Request so we pass an empty
	// actor (the request audit already records who initiated it)
	// and put the matching export_id in target_id so the two rows
	// link cleanly.
	if w.AuditSvc != nil {
		w.AuditSvc.Log(bctx, args.ProjectID, "", "",
			audit.ActionExportCompleted,
			audit.WithTarget("export", args.ExportID),
			audit.WithMetadata(map[string]any{
				"s3_key":    s3Key,
				"file_size": size,
				"rows":      totalRows,
				"scope":     "tenant",
				"complete":  result.Complete,
				"warnings":  result.Warnings,
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
	// Data / Dedicated: see TenantExportWorker.
	Data      compliance.ExportSource
	Dedicated DedicatedResolver
}

// Timeout overrides River's 1-minute default (#370).
func (w *UserExportWorker) Timeout(*river.Job[jobs.UserExportArgs]) time.Duration {
	return exportJobTimeout
}

func (w *UserExportWorker) Work(ctx context.Context, job *river.Job[jobs.UserExportArgs]) (err error) {
	args := job.Args
	logger := slog.With("export_id", args.ExportID, "project_id", args.ProjectID, "user_id", args.UserID, "type", "user")
	started := time.Now()
	defer func() {
		outcome := "completed"
		if err != nil {
			outcome = "failed"
		}
		logExportDuration(logger, started, outcome)
		err = exportResult(ctx, err)
	}()

	exportSvc := compliance.NewExportService(w.DBPool, w.S3, w.AuditSvc)
	failExport := func(stage string, err error) {
		final := exportAttemptFinal(ctx, job.Attempt, job.MaxAttempts)
		ctx, cancel := exportBookkeepingContext(ctx)
		defer cancel()
		if final {
			_ = exportSvc.MarkFailed(ctx, args.ExportID, err.Error())
		}
		if w.AuditSvc != nil {
			w.AuditSvc.Log(ctx, args.ProjectID, "", "",
				audit.ActionExportFailed,
				audit.WithTarget("export", args.ExportID),
				audit.WithMetadata(map[string]any{
					"scope":          "user",
					"target_user_id": args.UserID,
					"stage":          stage,
					"error":          err.Error(),
					"will_retry":     !final,
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
	if err := refuseDuringUpgrade(ctx, upgradeStatePool(w.Data, w.DBPool), args.ProjectID); err != nil {
		failExport("upgrade_in_progress", err)
		return err
	}
	src, closeSrc, err := resolveExportSource(ctx, w.Data, w.DBPool, w.Dedicated, args.ProjectID)
	if err != nil {
		failExport("resolve_database", err)
		return err
	}
	defer closeSrc()

	logger.Info("streaming user export zip to temp file")
	var result *compliance.ExportResult
	tmpFile, size, totalRows, err := streamExportToTempFile(
		ctx, args.ExportID,
		func(out io.Writer) (int, error) {
			res, err := compliance.WriteUserExport(ctx, w.DBPool, src, out, schemaName, args.ProjectID, args.UserID, args.ExportID, args.Format)
			if err != nil {
				return 0, err
			}
			result = res
			return res.TotalRows, nil
		},
	)
	if err != nil {
		failExport("build_zip", err)
		return fmt.Errorf("build zip: %w", err)
	}
	defer cleanupTempFile(tmpFile)

	s3Key := storage.ExportArchivePrefix(args.ProjectID) + "users/" + args.UserID + "/" + args.ExportID + ".zip"
	logger.Info("uploading user export to s3", "key", s3Key, "size", size, "rows", totalRows)

	if err := w.S3.UploadObject(ctx, s3Bucket, s3Key, tmpFile, "application/zip", size); err != nil {
		failExport("upload", err)
		return fmt.Errorf("upload: %w", err)
	}

	// The archive is uploaded: record it even if the job context expires
	// now, or remove it — never leave an archive no row points at.
	bctx, bcancel := exportBookkeepingContext(ctx)
	defer bcancel()
	if err := exportSvc.MarkCompleted(bctx, args.ExportID, s3Key, size, result); err != nil {
		if derr := w.S3.DeleteObject(bctx, s3Bucket, s3Key); derr != nil {
			logger.Error("export archive uploaded but not recorded, and could not be deleted", "key", s3Key, "error", derr)
		}
		failExport("mark_completed", err)
		return fmt.Errorf("mark completed: %w", err)
	}
	if !result.Complete {
		logger.Warn("user export is incomplete", "warnings", result.Warnings)
	}

	// Closes #100, per-user / self-serve variant.
	if w.AuditSvc != nil {
		w.AuditSvc.Log(bctx, args.ProjectID, "", "",
			audit.ActionExportCompleted,
			audit.WithTarget("export", args.ExportID),
			audit.WithMetadata(map[string]any{
				"s3_key":         s3Key,
				"file_size":      size,
				"rows":           totalRows,
				"scope":          "user",
				"target_user_id": args.UserID,
				"complete":       result.Complete,
				"warnings":       result.Warnings,
			}))
	}

	logger.Info("user export completed", "size", size, "rows", totalRows)
	return nil
}

// DedicatedResolver opens an owner pool on a project's live dedicated
// database (dbprovider.OpenOwnerPool). It returns (nil, nil) for a
// project without one — its data is on the shared cluster.
type DedicatedResolver func(ctx context.Context, projectID string) (*pgxpool.Pool, error)

// ErrUpgradeInProgress fails an export while the project is being upgraded
// to a Team tier: project_databases turns active at the end of provisioning,
// before the data copy and cutover, so the dedicated database would be read
// (and reported complete) while it is still empty or partly copied.
var ErrUpgradeInProgress = errors.New("the project is being upgraded to a dedicated database; retry the export once the upgrade is live")

// refuseDuringUpgrade returns ErrUpgradeInProgress while projectID has an
// upgrade before cutover (requested → cutting_over, the states the upgrade
// worker resumes). From 'live' on, live traffic uses the dedicated database,
// so that is what the export reads.
func refuseDuringUpgrade(ctx context.Context, pool *pgxpool.Pool, projectID string) error {
	var inFlight bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM public.project_upgrades
		  WHERE project_id = $1 AND state IN ('requested', 'provisioning', 'copying', 'cutting_over'))`,
		projectID,
	).Scan(&inFlight); err != nil {
		return fmt.Errorf("check for an upgrade in progress: %w", err)
	}
	if inFlight {
		return ErrUpgradeInProgress
	}
	return nil
}

// upgradeStatePool is the pool refuseDuringUpgrade reads project_upgrades
// with: the developer pool (data.Pool). Migration 000117 revokes the table
// from eurobase_gateway — the worker's DBPool — so reading it there fails
// every export with 42501. Without a developer pool (dev) it's fallback.
func upgradeStatePool(data compliance.ExportSource, fallback *pgxpool.Pool) *pgxpool.Pool {
	if data.Pool != nil {
		return data.Pool
	}
	return fallback
}

// resolveExportSource picks where a project's tenant tables are read from
// (#663). A Team-tier project with a live dedicated database is read there,
// as its owner: that's where live traffic reads and writes its data, while
// the shared cluster has no schema for it (or a stale pre-upgrade copy).
// Every other project is read from the shared cluster through data (the
// developer pool, #654), or through fallback without one (dev). An error —
// dedicated database not active yet, credential unavailable — fails the
// export rather than reading the wrong database. The returned func closes
// what was opened.
func resolveExportSource(ctx context.Context, data compliance.ExportSource, fallback *pgxpool.Pool, dedicated DedicatedResolver, projectID string) (compliance.ExportSource, func(), error) {
	noop := func() {}
	if dedicated != nil {
		p, err := dedicated(ctx, projectID)
		if err != nil {
			return compliance.ExportSource{}, noop, fmt.Errorf("open the project's dedicated database: %w", err)
		}
		if p != nil {
			return compliance.ExportSource{Pool: p}, p.Close, nil
		}
	}
	if data.Pool != nil {
		return data, noop, nil
	}
	return compliance.ExportSource{Pool: fallback}, noop, nil
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
