package workers

import (
	"context"
	"errors"
	"log/slog"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// UpgradeProjectWorker drives the Free/Pro → Team tier upgrade
// state machine. Enqueued by upgrade.Service.RequestUpgrade with
// jobs.UpgradeProjectArgs.
//
// STATE MACHINE (implemented incrementally):
//
//	requested → provisioning → copying → cutting_over → live → confirmed
//	                   ↓            ↓          ↓
//	                 failed      failed     failed
//
// This PR ships the skeleton: the worker exists so River can dispatch
// jobs.UpgradeProjectArgs without an "unregistered kind" error, but
// the actual provisioning + copy + cutover logic lands in the
// follow-up PR (team-tier/upgrade-worker-impl). For now the worker
// marks the row state='failed' with a clear error string so ops
// notices any stray enqueues.
//
// Once the follow-up ships, this file becomes the ~400-line state
// machine described in the plan at ~/.claude/plans/calm-dancing-lecun.md.
type UpgradeProjectWorker struct {
	river.WorkerDefaults[jobs.UpgradeProjectArgs]

	// Pool is the developer pool for reading/writing project_upgrades
	// rows. Runs SET LOCAL ROLE eurobase_migrator per statement, per
	// the same pattern used elsewhere for platform writes.
	Pool *pgxpool.Pool
}

func (w *UpgradeProjectWorker) Work(ctx context.Context, job *river.Job[jobs.UpgradeProjectArgs]) error {
	if w.Pool == nil {
		return river.JobCancel(errors.New("upgrade_project worker: pool not configured"))
	}

	logger := slog.With("upgrade_id", job.Args.UpgradeID)
	logger.Warn("upgrade_project worker: implementation pending — marking upgrade failed",
		"tracking", "team-tier/upgrade-worker-impl")

	// Mark the row failed so the admin UI + sweeper both see the
	// terminal state cleanly. Match the developer-role pattern used
	// by upgrade.Service.
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return river.JobCancel(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SET LOCAL ROLE eurobase_migrator`); err != nil {
		return river.JobCancel(err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.project_upgrades
		    SET state = 'failed',
		        failed_at = now(),
		        error = 'upgrade worker not yet implemented (skeleton PR); pending team-tier/upgrade-worker-impl'
		  WHERE id = $1
		    AND state = 'requested'`,
		job.Args.UpgradeID,
	); err != nil {
		return river.JobCancel(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return river.JobCancel(err)
	}

	// river.JobCancel keeps the row in River as cancelled — no retry
	// storm on the skeleton.
	return river.JobCancel(errors.New("upgrade worker not yet implemented"))
}
