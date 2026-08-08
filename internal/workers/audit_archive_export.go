package workers

// Tier-1 GDPR #170 — daily sweeper that dumps audit_log_archive to a
// platform-managed S3 bucket under Object Lock, then purges the
// archived rows.
//
// Closes the loop the retention path has been assuming existed
// (retention.go's "off-box WORM dump" comment). Same fixed-cadence
// housekeeping shape as StartAuditRetention / StartBackupSweeper —
// not a River job.
//
// Fail-safe wiring: if AUDIT_ARCHIVE_EXPORT_BUCKET is unset, the
// worker logs "not configured, skipping" and returns without
// spawning the ticker goroutine. This lets ops enable the exporter
// with an env-var flip; the archive keeps growing in the DB in the
// meantime (bounded by however long ops takes to set the var).

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

const auditArchiveExportInterval = 24 * time.Hour

// StartAuditArchiveExporter launches the daily dumper loop. Returns
// immediately; the loop exits when ctx is cancelled. Reads config
// once from env:
//   - AUDIT_ARCHIVE_EXPORT_BUCKET     (required — else disabled)
//   - AUDIT_ARCHIVE_EXPORT_PREFIX     (default "audit-log-archive/")
//   - AUDIT_ARCHIVE_RETENTION_YEARS   (default 10)
//   - AUDIT_ARCHIVE_EXPORT_BATCH_SIZE (default 5000)
func StartAuditArchiveExporter(ctx context.Context, pool *pgxpool.Pool, s3 *storage.S3Client) {
	bucket := os.Getenv("AUDIT_ARCHIVE_EXPORT_BUCKET")
	if bucket == "" {
		slog.Info("audit archive exporter: AUDIT_ARCHIVE_EXPORT_BUCKET not set, exporter disabled",
			"note", "audit_log_archive rows will accumulate until this is configured")
		return
	}
	if s3 == nil {
		slog.Warn("audit archive exporter: s3 client not configured, exporter disabled")
		return
	}
	cfg := audit.ArchiveExportConfig{
		Bucket:    bucket,
		KeyPrefix: os.Getenv("AUDIT_ARCHIVE_EXPORT_PREFIX"),
	}
	if v := os.Getenv("AUDIT_ARCHIVE_RETENTION_YEARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RetentionYears = n
		}
	}
	if v := os.Getenv("AUDIT_ARCHIVE_EXPORT_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BatchSize = n
		}
	}

	exporter := audit.NewArchiveExporter(pool, &s3WORMUploader{s3: s3}, cfg)

	go func() {
		// Initial run at pod start so a freshly-rolled worker doesn't
		// wait 24h before draining whatever's in the archive.
		if err := runArchiveExport(ctx, exporter); err != nil {
			slog.Error("audit archive exporter: initial run failed", "error", err)
		}
		t := time.NewTicker(auditArchiveExportInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runArchiveExport(ctx, exporter); err != nil {
					slog.Error("audit archive exporter: run failed", "error", err)
				}
			}
		}
	}()
	slog.Info("audit archive exporter started",
		"interval", auditArchiveExportInterval.String(),
		"bucket", cfg.Bucket,
		"prefix", cfg.KeyPrefix,
		"retention_years", cfg.RetentionYears,
	)
}

// runArchiveExport drains the archive one batch per tick — a
// backfilling deploy can catch up over successive ticks rather than
// blocking the goroutine on a giant single upload. If ops wants
// aggressive catch-up they can drop the interval or set a bigger
// batch size.
func runArchiveExport(ctx context.Context, e *audit.ArchiveExporter) error {
	res, err := e.Run(ctx)
	if err != nil {
		return err
	}
	if res != nil && res.RowsDumped > 0 {
		slog.Info("audit archive exporter: batch dumped",
			"rows", res.RowsDumped,
			"object_keys", res.ObjectKeys,
		)
	}
	return nil
}

// s3WORMUploader is the concrete adapter between audit.WORMUploader
// (interface declared in the audit package to avoid an
// audit → storage import cycle — storage's access-log middleware
// already imports audit) and storage.S3Client.UploadObjectWithRetention.
// Trivial forwarding shim; kept alongside the sweeper wire-up.
type s3WORMUploader struct {
	s3 *storage.S3Client
}

func (u *s3WORMUploader) Upload(ctx context.Context, bucket, key string, body io.Reader, contentType string, size int64, retention audit.WORMRetention) error {
	return u.s3.UploadObjectWithRetention(ctx, bucket, key, body, contentType, size, storage.Retention{
		Mode:        storage.RetentionMode(retention.Mode),
		RetainUntil: retention.RetainUntil,
	})
}
