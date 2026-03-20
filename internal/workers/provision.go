// Package workers defines River job workers for async provisioning tasks.
package workers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// ProvisionProjectWorker handles async provisioning: S3 bucket creation,
// API key generation, and status updates.
type ProvisionProjectWorker struct {
	river.WorkerDefaults[jobs.ProvisionProjectArgs]
	S3     *storage.S3Client
	DBPool *pgxpool.Pool
}

// Work executes the async provisioning steps for a project.
func (w *ProvisionProjectWorker) Work(ctx context.Context, job *river.Job[jobs.ProvisionProjectArgs]) error {
	args := job.Args
	logger := slog.With("project_id", args.ProjectID, "slug", args.Slug)

	logger.Info("starting async project provisioning")

	// Step 1: Create S3 bucket.
	bucketName := fmt.Sprintf("eurobase-%s", args.Slug)
	logger.Info("creating s3 bucket", "bucket", bucketName)
	if err := w.S3.CreateBucket(ctx, bucketName); err != nil {
		logger.Error("failed to create s3 bucket", "error", err)
		w.markFailed(ctx, args.ProjectID)
		return fmt.Errorf("create s3 bucket: %w", err)
	}

	// Step 2: Generate API key pair.
	logger.Info("generating api key pair")
	publicKey, secretKey, publicKeyHash, secretKeyHash, err := tenant.GenerateAPIKeyPair()
	if err != nil {
		logger.Error("failed to generate api keys", "error", err)
		w.markFailed(ctx, args.ProjectID)
		return fmt.Errorf("generate api keys: %w", err)
	}

	// We log the prefixes for debugging but never log full keys.
	_ = publicKey
	_ = secretKey

	// Step 3: Store API keys in a transaction.
	logger.Info("storing api keys in database")
	tx, err := w.DBPool.Begin(ctx)
	if err != nil {
		logger.Error("failed to begin transaction", "error", err)
		w.markFailed(ctx, args.ProjectID)
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Use key prefixes (first 14 chars: "eb_pk_" + 8 hex chars) for identification.
	publicKeyPrefix := publicKey[:14]
	secretKeyPrefix := secretKey[:14]

	if err := tenant.StoreAPIKeys(ctx, tx, args.ProjectID, publicKeyHash, publicKeyPrefix, secretKeyHash, secretKeyPrefix); err != nil {
		logger.Error("failed to store api keys", "error", err)
		w.markFailed(ctx, args.ProjectID)
		return fmt.Errorf("store api keys: %w", err)
	}

	// Step 4: Update project status to 'active'.
	logger.Info("updating project status to active")
	_, err = tx.Exec(ctx,
		`UPDATE projects SET status = 'active' WHERE id = $1 AND status = 'provisioning'`,
		args.ProjectID,
	)
	if err != nil {
		logger.Error("failed to update project status", "error", err)
		w.markFailed(ctx, args.ProjectID)
		return fmt.Errorf("update project status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("failed to commit transaction", "error", err)
		w.markFailed(ctx, args.ProjectID)
		return fmt.Errorf("commit tx: %w", err)
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
