package workers

// Team-tier M3 follow-up (#457) — reconcile-backup-schedule worker.
//
// One job per project_databases row where
// backup_schedule_applied_at IS NULL. Looks up the row + its
// project's plan, resolves retention days via plans.LimitsService,
// calls Provider.SetBackupSchedule, and on success stamps
// backup_schedule_applied_at=now() so the sweeper stops picking
// the row up.
//
// Enqueue sources:
//   - StartBackupScheduleSweeper (hourly, primary path)
//   - Could also be enqueued ad-hoc by ops tooling on a specific
//     project row after a manual schedule change on the Scaleway
//     dashboard.
//
// Failure semantics — mirror the deprovision worker:
//   - Config error (unknown provider, retention <= 0 for a
//     dedicated plan): river.JobCancel; the sweeper won't re-
//     enqueue because the underlying state doesn't change.
//   - Transient error (Scaleway 5xx during warmup, network flake):
//     return error → River backs off + retries (MaxAttempts=5).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// backupScheduleFrequencyHours is the daily cadence sent to
// Scaleway. Kept as a constant (not a plan_limits column) because
// nothing today needs finer control — Scaleway's own default is
// also daily, and hourly/weekly aren't customer-visible knobs.
// When they need to be, promote to plan_limits.backup_schedule_frequency
// and thread through here.
const backupScheduleFrequencyHours = 24

type ReconcileBackupScheduleWorker struct {
	river.WorkerDefaults[jobs.ReconcileBackupScheduleArgs]
	Registry *dbprovider.Registry
	Repo     *dbprovider.Repo
	Limits   *plans.LimitsService
}

func (w *ReconcileBackupScheduleWorker) Work(ctx context.Context, job *river.Job[jobs.ReconcileBackupScheduleArgs]) error {
	logger := slog.With(
		"project_database_id", job.Args.ProjectDatabaseID,
		"attempt", job.Attempt,
	)

	rec, err := w.Repo.Get(ctx, job.Args.ProjectDatabaseID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Row gone — nothing to do. Idempotent-success (e.g. the
			// project was deprovisioned between the sweep enqueue
			// and the worker pickup).
			logger.Info("project_databases row not found — treating as already reconciled")
			return nil
		}
		return fmt.Errorf("load row: %w", err)
	}
	logger = logger.With(
		"project_id", rec.ProjectID,
		"provider", rec.Provider,
		"provider_instance_id", rec.ProviderInstanceID,
	)

	// If another actor beat us to it (e.g. the provision worker
	// completed after this job was enqueued), skip. deleted_at set
	// = the row is being torn down; likewise skip.
	if rec.DeletedAt != nil {
		logger.Info("row marked deleted — skipping schedule reconcile")
		return nil
	}
	if rec.State != dbprovider.StateActive {
		return river.JobCancel(fmt.Errorf("refusing to set backup schedule on non-active row (state=%s)", rec.State))
	}

	provider, err := w.Registry.Get(rec.Provider)
	if err != nil {
		logger.Error("provider not registered", "error", err)
		return river.JobCancel(err)
	}

	// Resolve retention from the project's plan. LimitsService caches
	// per-plan for process lifetime; the lookup is one indexed DB
	// hit on cold cache.
	limits, err := w.Limits.GetProjectLimits(ctx, rec.ProjectID)
	if err != nil {
		return fmt.Errorf("plan limits lookup: %w", err)
	}
	if limits.BackupRetentionDays <= 0 {
		// Config regression the whole feature exists to close (matches
		// #456's floor in HandleCreateBackup). Cancel loudly so ops
		// sees it in slog + River UI — retrying won't help; the
		// plan_limits row itself needs a positive value first.
		logger.Error("refusing to set backup schedule with zero retention — plan_limits.backup_retention_days must be > 0 for a dedicated-DB plan",
			"plan", limits.Plan,
			"backup_retention_days", limits.BackupRetentionDays)
		return river.JobCancel(fmt.Errorf("plan %q has backup_retention_days=%d; must be > 0", limits.Plan, limits.BackupRetentionDays))
	}

	opts := dbprovider.SetBackupScheduleOpts{
		FrequencyHours: backupScheduleFrequencyHours,
		RetentionDays:  limits.BackupRetentionDays,
	}
	if err := provider.SetBackupSchedule(ctx, rec.ProviderInstanceID, opts); err != nil {
		if isNonRetryable(err) {
			// Config error surfaced by the provider (unknown instance,
			// unauthorized, etc.) — don't burn 5 attempts.
			logger.Error("provider SetBackupSchedule non-retryable", "error", err)
			return river.JobCancel(err)
		}
		// Transient — River backs off + retries.
		return fmt.Errorf("provider SetBackupSchedule: %w", err)
	}

	if err := w.Repo.MarkBackupScheduleApplied(ctx, rec.ID); err != nil {
		// Provider is already correctly configured; local bookkeeping
		// failed. Retry the whole thing — SetBackupSchedule is
		// idempotent so the next attempt is safe.
		return fmt.Errorf("mark applied: %w", err)
	}
	logger.Info("backup schedule reconciled",
		"retention_days", limits.BackupRetentionDays,
		"frequency_hours", backupScheduleFrequencyHours)
	return nil
}
