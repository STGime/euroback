// Package workers defines River job workers for async provisioning tasks.
package workers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// ProvisionProjectWorker handles async provisioning: S3 bucket creation.
// API key generation has been moved to the synchronous CreateProject flow.
type ProvisionProjectWorker struct {
	river.WorkerDefaults[jobs.ProvisionProjectArgs]
	S3     *storage.S3Client
	DBPool *pgxpool.Pool
}

// Work executes the async provisioning steps for a project.
func (w *ProvisionProjectWorker) Work(ctx context.Context, job *river.Job[jobs.ProvisionProjectArgs]) error {
	args := job.Args
	logger := slog.With("project_id", args.ProjectID, "slug", args.Slug)

	logger.Info("starting async project provisioning (s3 bucket)")

	// Create S3 bucket.
	bucketName := fmt.Sprintf("eurobase-%s", args.Slug)
	logger.Info("creating s3 bucket", "bucket", bucketName)
	if err := w.S3.CreateBucket(ctx, bucketName); err != nil {
		logger.Error("failed to create s3 bucket", "error", err)
		w.markFailed(ctx, args.ProjectID)
		return fmt.Errorf("create s3 bucket: %w", err)
	}

	logger.Info("async project provisioning completed successfully")
	return nil
}

// markFailed updates the project status to 'provisioning_failed'.
func (w *ProvisionProjectWorker) markFailed(ctx context.Context, projectID string) {
	_, err := w.DBPool.Exec(ctx,
		`UPDATE projects SET status = 'provisioning_failed' WHERE id = $1`,
		projectID,
	)
	if err != nil {
		slog.Error("failed to mark project as provisioning_failed",
			"project_id", projectID,
			"error", err,
		)
	}
}
