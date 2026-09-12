package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// UpgradeProjectWorker drives the Free/Pro → Team tier upgrade
// state machine. Enqueued by upgrade.Service.RequestUpgrade with
// jobs.UpgradeProjectArgs.
//
// STATE MACHINE (this PR: real transitions; data copy STUBBED):
//
//	requested → provisioning → copying → cutting_over → live → confirmed
//	                   ↓            ↓          ↓
//	                 failed      failed     failed
//
// Concrete work per state:
//
//   - requested: initial state set by upgrade.Service. Worker
//     first thing on entry: flip projects.maintenance_mode = true
//     (freezes SDK writes for the whole upgrade), then advance to
//     provisioning.
//   - provisioning: call ProvisionTeamDatabaseWorker.Ensure with a
//     stable idempotency key derived from the upgrade ID. Populate
//     project_upgrades.project_database_id.
//   - copying: data copy is STUBBED in this PR — the state machine
//     transitions cleanly but the dedicated instance has zero user
//     data. Real work in follow-up team-tier/upgrade-worker-copy:
//     1. Replay public.tenant_migrations against the dedicated DB
//     2. Copy per-project public.* rows (edge_functions, cron_jobs,
//        email_templates, function_triggers, backup_snapshots)
//     3. Copy tenant schema data (COPY BINARY, FK-ordered)
//   - cutting_over: 5-sec drain wait, flip projects.plan to the
//     target tier, clear maintenance_mode, stamp cutover_at.
//   - live: return nil. Data remains on the shared cluster as a
//     rollback buffer until the confirmation sweeper (PR 6) or an
//     admin confirm request purges it.
//
// On any error, the worker marks state='failed' with the error
// message + clears maintenance_mode (so the project isn't stuck at
// 503), and returns a River error. MaxAttempts=3 gives a couple of
// retries for transient failures before ops has to intervene.
type UpgradeProjectWorker struct {
	river.WorkerDefaults[jobs.UpgradeProjectArgs]

	// Pool is the platform pool. All writes on project_upgrades /
	// projects run through it with SET LOCAL ROLE eurobase_migrator
	// inside each tx.
	Pool *pgxpool.Pool

	// Provisioner is the ProvisionTeamDatabaseWorker constructed at
	// startup. We reuse its Ensure() method (extracted in PR #560)
	// for the provisioning step so the upgrade path shares every
	// bit of the born-Team code — cipher unwrap, Scaleway
	// idempotency, bootstrap runtime + readonly credentials.
	//
	// Nil-safe: if unset the worker fails the upgrade cleanly with
	// "provisioner not configured" (cmd/worker/main.go wires it).
	Provisioner *ProvisionTeamDatabaseWorker
}

// upgradeMaintenanceDrain is the wait after maintenance_mode=true and
// before running the final incremental copy at cutover. Gives
// in-flight SDK requests a grace window to complete.
const upgradeMaintenanceDrain = 5 * time.Second

func (w *UpgradeProjectWorker) Work(ctx context.Context, job *river.Job[jobs.UpgradeProjectArgs]) error {
	if w.Pool == nil {
		return river.JobCancel(errors.New("upgrade_project worker: pool not configured"))
	}

	upgradeID := job.Args.UpgradeID
	logger := slog.With("upgrade_id", upgradeID)

	row, err := w.loadUpgrade(ctx, upgradeID)
	if err != nil {
		logger.Error("load upgrade row failed", "error", err)
		return err
	}
	logger = logger.With("project_id", row.projectID, "to_plan", row.toPlan)

	// Guard: only proceed if state is a live (non-terminal) one. An
	// admin abort or a prior worker attempt may have flipped it to
	// failed/confirmed already; do not touch.
	switch row.state {
	case "requested", "provisioning", "copying", "cutting_over":
		// resumable — fall through
	default:
		logger.Warn("upgrade already in terminal state — skipping", "state", row.state)
		return river.JobCancel(fmt.Errorf("upgrade %s in terminal state %q", upgradeID, row.state))
	}

	// Enter maintenance + advance to provisioning on first entry.
	// Idempotent via WHERE state='requested' — a retry after prior
	// success no-ops.
	if row.state == "requested" {
		if err := w.enterMaintenance(ctx, row.projectID); err != nil {
			return w.fail(ctx, logger, upgradeID, "enter maintenance", err)
		}
		if err := w.updateState(ctx, upgradeID, "provisioning", "requested"); err != nil {
			return w.fail(ctx, logger, upgradeID, "advance to provisioning", err)
		}
		row.state = "provisioning"
		logger.Info("upgrade state: provisioning (maintenance on)")
	}

	// Provisioning step. Ensure() is idempotent + returns the active
	// record (host, port, runtime creds).
	//
	// job.Attempt is forwarded so a River retry after
	// state=active-on-project_databases-but-bootstrap-failed triggers
	// the resume-from-active branch (provision_team_db.go:176). Without
	// this, a retry would re-run Provision + InsertProvisioning and
	// stall permanently on the state='active' partial unique index —
	// the exact regression PR #560's doc warned about.
	if row.state == "provisioning" {
		if w.Provisioner == nil {
			return w.fail(ctx, logger, upgradeID, "provisioner", errors.New("Provisioner not configured"))
		}
		idemKey := fmt.Sprintf("upgrade-%s", upgradeID)
		rec, err := w.Provisioner.Ensure(ctx, jobs.ProvisionTeamDatabaseArgs{
			ProjectID: row.projectID,
			Slug:      row.projectSlug,
			Provider:  "scaleway",
			Region:    "fr-par",
			Size:      "small",
		}, job.Attempt, idemKey)
		if err != nil {
			return w.fail(ctx, logger, upgradeID, "provision dedicated instance", err)
		}
		if err := w.markProvisioned(ctx, upgradeID, rec.ID); err != nil {
			return w.fail(ctx, logger, upgradeID, "mark provisioned", err)
		}
		row.state = "copying"
		logger.Info("upgrade state: copying (dedicated instance ready)",
			"project_database_id", rec.ID,
			"host", rec.Host)
	}

	// Copying step. STUBBED in this PR — see the type doc comment
	// for the real work due in team-tier/upgrade-worker-copy.
	if row.state == "copying" {
		logger.Warn("upgrade data-copy STUBBED — dedicated instance will have zero user data until follow-up PR lands",
			"tracking", "team-tier/upgrade-worker-copy")
		if err := w.updateState(ctx, upgradeID, "cutting_over", "copying"); err != nil {
			return w.fail(ctx, logger, upgradeID, "advance to cutting_over", err)
		}
		if err := w.stampCopied(ctx, upgradeID); err != nil {
			logger.Warn("stamp copied_at failed — non-fatal", "error", err)
		}
		row.state = "cutting_over"
		logger.Info("upgrade state: cutting_over")
	}

	// Cutting-over step. Drain → flip plan → clear maintenance →
	// state=live.
	if row.state == "cutting_over" {
		select {
		case <-time.After(upgradeMaintenanceDrain):
		case <-ctx.Done():
			return w.fail(ctx, logger, upgradeID, "cutover drain interrupted", ctx.Err())
		}

		if err := w.flipPlan(ctx, row.projectID, row.toPlan); err != nil {
			return w.fail(ctx, logger, upgradeID, "flip projects.plan", err)
		}
		if err := w.exitMaintenance(ctx, row.projectID); err != nil {
			// Loud but non-fatal — the plan is flipped, maintenance
			// mode being briefly stuck can be cleared by ops via SQL.
			logger.Error("clear maintenance_mode failed at cutover — ops may need to clear via SQL",
				"project_id", row.projectID, "error", err)
		}
		if err := w.updateStateAndCutoverAt(ctx, upgradeID, "live", "cutting_over"); err != nil {
			return w.fail(ctx, logger, upgradeID, "advance to live", err)
		}
		logger.Info("upgrade state: live (project now on dedicated instance)")
	}

	return nil
}

// upgradeRow is what loadUpgrade returns.
type upgradeRow struct {
	projectID   string
	projectSlug string
	state       string
	fromPlan    string
	toPlan      string
}

func (w *UpgradeProjectWorker) loadUpgrade(ctx context.Context, upgradeID string) (*upgradeRow, error) {
	var r upgradeRow
	err := w.Pool.QueryRow(ctx,
		`SELECT u.project_id, p.slug, u.state, u.from_plan, u.to_plan
		   FROM public.project_upgrades u
		   JOIN public.projects p ON p.id = u.project_id
		  WHERE u.id = $1`,
		upgradeID,
	).Scan(&r.projectID, &r.projectSlug, &r.state, &r.fromPlan, &r.toPlan)
	if err != nil {
		return nil, fmt.Errorf("load upgrade row: %w", err)
	}
	return &r, nil
}

func (w *UpgradeProjectWorker) enterMaintenance(ctx context.Context, projectID string) error {
	return w.execWithMigratorRole(ctx,
		`UPDATE public.projects SET maintenance_mode = true WHERE id = $1`,
		projectID)
}

func (w *UpgradeProjectWorker) exitMaintenance(ctx context.Context, projectID string) error {
	return w.execWithMigratorRole(ctx,
		`UPDATE public.projects SET maintenance_mode = false WHERE id = $1`,
		projectID)
}

// flipPlan sets projects.plan to the target Team tier. Guarded on
// current plan being free/pro so a concurrent admin change surfaces
// as an error. Idempotent across retries: if the plan is already
// flipped to `toPlan` (previous cutover attempt succeeded but the
// subsequent state stamp failed), this returns nil without error so
// the retry can move on to updateStateAndCutoverAt.
func (w *UpgradeProjectWorker) flipPlan(ctx context.Context, projectID, toPlan string) error {
	tag, err := w.execAndCount(ctx,
		`UPDATE public.projects
		    SET plan = $1
		  WHERE id = $2 AND plan IN ('free', 'pro')`,
		toPlan, projectID)
	if err != nil {
		return err
	}
	if tag == 1 {
		return nil
	}
	// Zero rows affected — either concurrent change or already-flipped.
	// Read current plan to distinguish. Already-flipped is fine for a
	// retry; anything else is an error the operator needs to see.
	var currentPlan string
	if err := w.Pool.QueryRow(ctx,
		`SELECT plan FROM public.projects WHERE id = $1`,
		projectID,
	).Scan(&currentPlan); err != nil {
		return fmt.Errorf("plan flip affected 0 rows and follow-up read failed: %w", err)
	}
	if currentPlan == toPlan {
		// Idempotent success: the row was already at toPlan (previous
		// cutover attempt commit-then-crashed between flipPlan and
		// updateStateAndCutoverAt).
		return nil
	}
	return fmt.Errorf("plan flip affected 0 rows and current plan=%q (expected free/pro or already %q) — concurrent change?",
		currentPlan, toPlan)
}

func (w *UpgradeProjectWorker) updateState(ctx context.Context, upgradeID, newState, expected string) error {
	tag, err := w.execAndCount(ctx,
		`UPDATE public.project_upgrades SET state = $1 WHERE id = $2 AND state = $3`,
		newState, upgradeID, expected)
	if err != nil {
		return err
	}
	if tag != 1 {
		return fmt.Errorf("state transition %q → %q affected %d rows (expected 1)", expected, newState, tag)
	}
	return nil
}

func (w *UpgradeProjectWorker) updateStateAndCutoverAt(ctx context.Context, upgradeID, newState, expected string) error {
	tag, err := w.execAndCount(ctx,
		`UPDATE public.project_upgrades
		    SET state = $1, cutover_at = now()
		  WHERE id = $2 AND state = $3`,
		newState, upgradeID, expected)
	if err != nil {
		return err
	}
	if tag != 1 {
		return fmt.Errorf("state transition %q → %q affected %d rows (expected 1)", expected, newState, tag)
	}
	return nil
}

func (w *UpgradeProjectWorker) markProvisioned(ctx context.Context, upgradeID, projectDatabaseID string) error {
	tag, err := w.execAndCount(ctx,
		`UPDATE public.project_upgrades
		    SET state = 'copying',
		        project_database_id = $1,
		        provisioned_at = now()
		  WHERE id = $2 AND state = 'provisioning'`,
		projectDatabaseID, upgradeID)
	if err != nil {
		return err
	}
	if tag != 1 {
		return fmt.Errorf("mark provisioned affected %d rows (expected 1)", tag)
	}
	return nil
}

func (w *UpgradeProjectWorker) stampCopied(ctx context.Context, upgradeID string) error {
	_, err := w.execAndCount(ctx,
		`UPDATE public.project_upgrades SET copied_at = now() WHERE id = $1`,
		upgradeID)
	return err
}

// fail marks the upgrade failed and clears maintenance_mode as a
// side effect so the project isn't stuck at 503. Called from every
// error path in Work().
func (w *UpgradeProjectWorker) fail(ctx context.Context, logger *slog.Logger, upgradeID, step string, cause error) error {
	logger.Error("upgrade failed", "step", step, "error", cause)

	// Fresh context so a canceled parent doesn't prevent recording
	// the terminal state or clearing maintenance.
	freshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if err := w.execWithMigratorRole(freshCtx,
		`UPDATE public.project_upgrades
		    SET state = 'failed',
		        failed_at = now(),
		        error = $1
		  WHERE id = $2 AND state NOT IN ('confirmed', 'failed')`,
		fmt.Sprintf("%s: %v", step, cause), upgradeID,
	); err != nil {
		logger.Error("mark failed failed", "error", err)
	}

	// Clear maintenance_mode. Best effort — ops can clear via SQL.
	var projectID string
	if err := w.Pool.QueryRow(freshCtx,
		`SELECT project_id FROM public.project_upgrades WHERE id = $1`,
		upgradeID,
	).Scan(&projectID); err == nil {
		_ = w.exitMaintenance(freshCtx, projectID)
	}

	return fmt.Errorf("%s: %w", step, cause)
}

func (w *UpgradeProjectWorker) execWithMigratorRole(ctx context.Context, sql string, args ...any) error {
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE eurobase_migrator`); err != nil {
		return fmt.Errorf("set migrator role: %w", err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *UpgradeProjectWorker) execAndCount(ctx context.Context, sql string, args ...any) (int64, error) {
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE eurobase_migrator`); err != nil {
		return 0, fmt.Errorf("set migrator role: %w", err)
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
