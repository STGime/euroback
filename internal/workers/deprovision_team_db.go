package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// DeprovisionTeamDatabaseWorker tears down a dedicated managed-PG
// instance whose project_databases row was marked deleted_at more
// than 7 days ago (the two-instance-restore rollback window).
//
// Two entry points:
//   * one-off — enqueue DeprovisionTeamDatabaseArgs{ProjectDatabaseID}
//     directly (used by ops tooling or when a project is fully torn
//     down).
//   * cron — a periodic sweep (M3 issue #323) enqueues one job per
//     eligible row.
//
// Idempotent: a 404 from the provider is treated as success (see
// Scaleway.Delete). If the row is already hard-deleted we return
// nil without an error.
type DeprovisionTeamDatabaseWorker struct {
	river.WorkerDefaults[jobs.DeprovisionTeamDatabaseArgs]
	Registry *dbprovider.Registry
	Repo     *dbprovider.Repo
	// RollbackWindow is the minimum age (based on deleted_at) before
	// a row is eligible for hard delete. Defaults to the shared
	// deprovisionRollbackWindow constant (see deprovision_sweeper.go)
	// if zero — matches the plan. Guard against callers enqueuing
	// a job for a row still inside its rollback window.
	RollbackWindow time.Duration
}

func (w *DeprovisionTeamDatabaseWorker) Work(ctx context.Context, job *river.Job[jobs.DeprovisionTeamDatabaseArgs]) error {
	logger := slog.With("project_database_id", job.Args.ProjectDatabaseID)

	rec, err := w.Repo.Get(ctx, job.Args.ProjectDatabaseID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Row gone — nothing to do. Idempotent success case
			// (e.g. two racing sweep runs).
			logger.Info("project_databases row not found — treating as already deprovisioned")
			return nil
		}
		return fmt.Errorf("load row: %w", err)
	}

	logger = logger.With(
		"project_id", rec.ProjectID,
		"provider", rec.Provider,
		"provider_instance_id", rec.ProviderInstanceID,
	)

	// Refuse to delete rows that aren't past their rollback window.
	// The cron sweep filters at query time, but a manually-enqueued
	// job could arrive too early — a defence-in-depth check.
	window := w.RollbackWindow
	if window == 0 {
		window = deprovisionRollbackWindow
	}
	// Wrap invariant violations in river.JobCancel so River doesn't
	// retry all 5 attempts against a condition that cannot heal
	// (deleted_at doesn't flip while the job body sits in the queue,
	// and River's total backoff is ~16 min vs a 7-day rollback
	// window). Bug_004 from PR #331 review.
	if rec.DeletedAt == nil {
		return river.JobCancel(fmt.Errorf("refusing to deprovision live row: deleted_at is NULL for %s", rec.ID))
	}
	if time.Since(*rec.DeletedAt) < window {
		return river.JobCancel(fmt.Errorf("refusing to deprovision row still in %s rollback window (deleted_at=%s)",
			window, rec.DeletedAt.Format(time.RFC3339)))
	}

	provider, err := w.Registry.Get(rec.Provider)
	if err != nil {
		logger.Error("provider not registered", "error", err)
		return river.JobCancel(err)
	}

	logger.Info("deleting managed-PG instance at provider")
	if err := provider.Delete(ctx, rec.ProviderInstanceID); err != nil {
		if isNonRetryable(err) {
			logger.Error("provider delete failed non-retryably — leaving row for manual cleanup", "error", err)
			return river.JobCancel(err)
		}
		return fmt.Errorf("provider delete: %w", err)
	}

	if err := w.Repo.HardDelete(ctx, rec.ID); err != nil {
		// The instance is gone at the provider, so we can't retry
		// the provider delete — but we CAN retry the row delete
		// against a transient DB blip.
		return fmt.Errorf("hard-delete row: %w", err)
	}
	logger.Info("team-tier database deprovisioned + row hard-deleted")
	return nil
}

