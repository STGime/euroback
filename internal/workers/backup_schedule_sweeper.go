package workers

// Team-tier M3 follow-up (#457) — backup-schedule sweeper.
//
// Hourly tick that walks project_databases WHERE state='active'
// AND deleted_at IS NULL AND backup_schedule_applied_at IS NULL
// and fans out one ReconcileBackupScheduleArgs per matching row.
//
// **Why this exists:** #457 wires SetBackupSchedule into the
// provision worker so new Team-tier instances get an explicit
// Scaleway backup schedule (30-day retention for Team + Legal
// Team). But two edge cases would still leave rows on Scaleway
// defaults:
//   1. Instances provisioned BEFORE #457 landed — their
//      backup_schedule_applied_at is NULL and nothing else clears
//      it.
//   2. New provisions where the inline SetBackupSchedule call
//      failed transiently (Scaleway may reject during instance
//      warmup). The provision worker's inline call is
//      log-and-continue; without this sweeper, that row stays on
//      Scaleway defaults until manual intervention — the exact
//      hazard the reviewer flagged on the issue.
//
// This sweeper's first tick after deploy discovers both classes
// at once and drains them (bounded to 100/tick via
// Repo.ListNeedsBackupSchedule).
//
// Idempotency:
//   - Sweep re-enqueue is collapsed by ReconcileBackupScheduleArgs's
//     UniqueOpts{ByArgs:true}.
//   - Worker's MarkBackupScheduleApplied stamps the row on success;
//     next sweep tick's WHERE excludes it.
//   - Provider.SetBackupSchedule is idempotent on Scaleway (same
//     value → same result, no side effects), so even a partial
//     apply-then-mark race can't produce inconsistent state.
//
// Not a River job — same rationale as StartDeprovisionSweeper /
// StartBackupSweeper: fixed-cadence housekeeping.

import (
	"context"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const (
	backupScheduleSweeperInterval = 1 * time.Hour
	backupScheduleBatchSize       = 100
)

// StartBackupScheduleSweeper launches the once-per-hour sweeper.
// Returns immediately; loop exits when ctx is cancelled.
//
// One-interval startup delay — matches StartDeprovisionSweeper.
// Team-tier provisioning cadence is measured in days, so waiting
// an hour at pod-start costs nothing and avoids racing River's
// startup.
func StartBackupScheduleSweeper(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) {
	go func() {
		t := time.NewTicker(backupScheduleSweeperInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runBackupScheduleSweeper(ctx, pool, riverClient); err != nil {
					slog.Error("backup-schedule sweeper: run failed", "error", err)
				}
			}
		}
	}()
	slog.Info("backup-schedule sweeper started",
		"interval", backupScheduleSweeperInterval,
		"batch", backupScheduleBatchSize)
}

func runBackupScheduleSweeper(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) error {
	repo := dbprovider.NewRepo(pool)
	return runBackupScheduleSweeperWithDeps(ctx,
		func(ctx context.Context, limit int) ([]dbprovider.BackupScheduleCandidate, error) {
			return repo.ListNeedsBackupSchedule(ctx, limit)
		},
		func(ctx context.Context, projectDatabaseID string) error {
			_, err := riverClient.Insert(ctx, jobs.ReconcileBackupScheduleArgs{
				ProjectDatabaseID: projectDatabaseID,
			}, nil)
			return err
		},
		backupScheduleBatchSize,
	)
}

// runBackupScheduleSweeperWithDeps is the pool-and-River-less
// inner loop — same seam pattern as runDeprovisionSweeperWithDeps
// (#455 review response). Fakes drive the fan-out without a live
// Postgres or River client.
func runBackupScheduleSweeperWithDeps(
	ctx context.Context,
	list func(ctx context.Context, limit int) ([]dbprovider.BackupScheduleCandidate, error),
	enqueue func(ctx context.Context, projectDatabaseID string) error,
	batch int,
) error {
	rows, err := list(ctx, batch)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		// Silent no-op — once the initial backlog drains this is the
		// steady state.
		return nil
	}
	slog.Info("backup-schedule sweeper: enqueuing", "count", len(rows))
	for _, r := range rows {
		if err := enqueue(ctx, r.ID); err != nil {
			slog.Error("backup-schedule sweeper: enqueue failed",
				"project_database_id", r.ID, "error", err)
			// keep going — one bad enqueue shouldn't stop the batch
		}
	}
	return nil
}
