package workers

// Team-tier M3 — deprovision sweeper.
//
// Once-per-hour tick that walks project_databases WHERE deleted_at
// IS NOT NULL AND deleted_at < now() - 7 days, and fans out one
// DeprovisionTeamDatabaseArgs job per matching row.
//
// **Why this exists:** without it, every restore permanently
// leaves the OLD Scaleway RDB instance running. The restore
// worker's cutover step deliberately keeps the old instance for
// a 7-day rollback window (see internal/workers/restore_team_db.go
// step 3, and project_databases.superseded_by / deleted_at). The
// deprovision worker itself (deprovision_team_db.go) is fully
// implemented, but until this sweeper existed nothing was actually
// enqueuing jobs against it — the old instances sat billing
// forever. A single Team user doing 3 restores would leave 4
// managed-PG instances live, ~€60/mo each on Scaleway. That's the
// hole this closes.
//
// Bounded batch size (100 per tick, matches
// Repo.ListDeprovisionCandidates default LIMIT) — a Team-tier
// backlog can only ever be as large as (# projects) × (recent
// restore attempts), which is small in practice.
//
// Idempotency:
//   - The DB worker's own guards refuse to touch rows still inside
//     the rollback window (deleted_at IS NULL, or deleted_at
//     newer than 7d) — belt-and-braces if a job somehow lands
//     early.
//   - DeprovisionTeamDatabaseArgs carries UniqueOpts{ByArgs:true}
//     so a double-enqueue from two overlapping ticks collapses
//     into one River job.
//   - Provider.Delete() treats 404 as success, so the worker itself
//     is idempotent against a partial prior success.
//
// Not a River job — same reasoning as StartBackupSweeper /
// StartAuditRetention: this is fixed-cadence housekeeping with no
// upstream trigger.

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
	deprovisionSweeperInterval = 1 * time.Hour
	// deprovisionRollbackWindow is the SHARED 7-day rollback window
	// used by both this sweeper (as the eligibility query bound)
	// and the DeprovisionTeamDatabaseWorker (as its defence-in-depth
	// cancel-guard when RollbackWindow is zero on the worker struct).
	// One constant so the two can't drift — a mismatch would be safe
	// (worker cancels early enqueues) but noisy.
	deprovisionRollbackWindow = 7 * 24 * time.Hour
)

// StartDeprovisionSweeper launches the once-per-hour sweeper.
// Returns immediately; loop exits when ctx is cancelled.
//
// The initial run is delayed by one interval rather than fired
// immediately — the rollback window is 7 days, so a one-hour
// delay at pod start is invisible to the cost story; and it
// avoids racing River's own startup on a fresh worker pod.
func StartDeprovisionSweeper(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) {
	go func() {
		t := time.NewTicker(deprovisionSweeperInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runDeprovisionSweeper(ctx, pool, riverClient); err != nil {
					slog.Error("deprovision sweeper: run failed", "error", err)
				}
			}
		}
	}()
	slog.Info("deprovision sweeper started",
		"interval", deprovisionSweeperInterval,
		"rollback_window", deprovisionRollbackWindow)
}

func runDeprovisionSweeper(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) error {
	repo := dbprovider.NewRepo(pool)
	return runDeprovisionSweeperWithDeps(ctx,
		func(ctx context.Context, window time.Duration) ([]dbprovider.Record, error) {
			return repo.ListDeprovisionCandidates(ctx, window)
		},
		func(ctx context.Context, projectDatabaseID string) error {
			_, err := riverClient.Insert(ctx, jobs.DeprovisionTeamDatabaseArgs{
				ProjectDatabaseID: projectDatabaseID,
			}, nil)
			return err
		},
		deprovisionRollbackWindow,
	)
}

// runDeprovisionSweeperWithDeps is the pool-and-River-less inner
// loop. Split out so the enqueue fan-out can be unit-tested
// without a live Postgres or River client — the two dependencies
// come in as function values (lister + enqueuer), which fakes can
// satisfy with a couple of lines each. `runDeprovisionSweeper`
// above is the thin wrapper that wires the real implementations.
func runDeprovisionSweeperWithDeps(
	ctx context.Context,
	list func(ctx context.Context, window time.Duration) ([]dbprovider.Record, error),
	enqueue func(ctx context.Context, projectDatabaseID string) error,
	window time.Duration,
) error {
	rows, err := list(ctx, window)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		// No work — silent. Avoids periodic "found 0 rows" noise
		// in the common case where nobody has restored recently.
		return nil
	}
	slog.Info("deprovision sweeper: enqueuing",
		"count", len(rows),
		"rollback_window", window)
	for _, r := range rows {
		if err := enqueue(ctx, r.ID); err != nil {
			slog.Error("deprovision sweeper: enqueue failed",
				"project_database_id", r.ID, "error", err)
			// keep going — one bad enqueue shouldn't stop the batch
		}
	}
	return nil
}
