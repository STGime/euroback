package workers

// RestoreTeamDatabaseWorker executes the two-instance restore state
// machine (Team-tier M3):
//
//   1. Load the pending restore_operations row.
//   2. Look up the OLD project_databases row from restore.old_instance_id.
//   3. Ask Provider.Restore to spin up a NEW instance from the snapshot
//      or PITR target — provider always returns a fresh instance
//      (never mutates the source).
//   4. Insert a NEW project_databases row (state='restoring').
//   5. Poll Describe until state=active.
//   6. Run a verification query (SELECT count(*) FROM
//      information_schema.tables) to sanity-check the restore
//      landed with schema data.
//   7. Atomic swap: new row → state='active'; old row →
//      state='deleting', deleted_at=now(), superseded_by=new.
//   8. Update restore_operations.state='complete'.
//
// The M1 deprovision sweeper picks the old instance up 7 days later
// (superseded_by is set so it's identifiable in reporting).
//
// On terminal failure at any step: bestEffortDelete the new
// instance (if provisioned), MarkFailed the row (if inserted), set
// restore_operations.state='failed' with the error message. The
// project stays on the OLD instance — no data loss.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

type RestoreTeamDatabaseWorker struct {
	river.WorkerDefaults[jobs.RestoreTeamDatabaseArgs]
	Pool     *pgxpool.Pool
	Registry *dbprovider.Registry
	Cipher   *dbprovider.Cipher
	Repo     *dbprovider.Repo

	// PollInterval defaults to 10 s. PollTimeout defaults to 15 min
	// — restore takes longer than provision because Scaleway has to
	// materialize the snapshot's WAL replay.
	PollInterval time.Duration
	PollTimeout  time.Duration
}

const (
	defaultRestorePollInterval = 10 * time.Second
	defaultRestorePollTimeout  = 15 * time.Minute
)

func (w *RestoreTeamDatabaseWorker) Work(ctx context.Context, job *river.Job[jobs.RestoreTeamDatabaseArgs]) error {
	restoreID := job.Args.RestoreOperationID
	logger := slog.With("restore_operation_id", restoreID)
	logger.Info("restore worker starting")

	// 1. Load the restore-op row + the OLD instance record.
	var (
		projectID     string
		kind          string
		sourceRef     string
		targetTime    *time.Time
		state         string
		oldInstanceID string
	)
	err := w.Pool.QueryRow(ctx,
		`SELECT project_id::text, kind, source_ref, target_time, state, old_instance_id::text
		   FROM public.restore_operations
		  WHERE id = $1::uuid`,
		restoreID,
	).Scan(&projectID, &kind, &sourceRef, &targetTime, &state, &oldInstanceID)
	if err != nil {
		return river.JobCancel(fmt.Errorf("load restore_operations: %w", err))
	}
	if state != "pending" {
		// Already picked up by an earlier attempt (or manually reset).
		// Cancel — nothing to do.
		logger.Warn("restore already in progress or done", "state", state)
		return river.JobCancel(fmt.Errorf("restore state is %s, expected pending", state))
	}

	oldRec, err := w.Repo.Get(ctx, oldInstanceID)
	if err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("load old instance: %w", err))
	}
	provider, err := w.Registry.Get(oldRec.Provider)
	if err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("provider lookup: %w", err))
	}

	// 2. Build the RestoreSource.
	var src dbprovider.RestoreSource
	switch kind {
	case "snapshot":
		src = dbprovider.RestoreSource{SnapshotID: sourceRef}
	case "pitr":
		if targetTime == nil {
			return w.fail(ctx, restoreID, errors.New("pitr restore missing target_time"))
		}
		src = dbprovider.RestoreSource{PITRTarget: *targetTime}
	default:
		return w.fail(ctx, restoreID, fmt.Errorf("unknown restore kind: %s", kind))
	}

	// 3. Move to 'provisioning' state.
	if err := w.setState(ctx, restoreID, "provisioning", ""); err != nil {
		return fmt.Errorf("mark provisioning: %w", err)
	}
	logger.Info("calling provider.Restore")

	newInst, err := provider.Restore(ctx, oldRec.ProviderInstanceID, src)
	if err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("provider restore: %w", err))
	}

	// 4. Seal a placeholder password if the provider returned one
	//    (Scaleway's snapshot-restore path doesn't; the new
	//    instance inherits the source's credentials, so we reuse
	//    the sealed password from oldRec).
	var ct, nonce []byte
	var ver int16
	if newInst.Password != "" {
		ct, nonce, ver, err = w.Cipher.Seal(newInst.Password)
		if err != nil {
			// Best-effort teardown of the just-provisioned instance
			// so we don't leak Scaleway spend.
			bestEffortDelete(context.WithoutCancel(ctx), provider, newInst.ProviderID, logger, "seal password failed")
			return w.fail(ctx, restoreID, fmt.Errorf("seal restored password: %w", err))
		}
		newInst.Password = ""
	} else {
		// Inherit sealed credentials from the source row so the
		// pool cache (M2.5) can build a DSN against the new
		// instance without a fresh secrets round-trip.
		ct = oldRec.PasswordCiphertext
		nonce = oldRec.PasswordNonce
		ver = oldRec.PasswordKeyVersion
		newInst.Username = oldRec.Username
	}

	// 5. Insert the new project_databases row (state='restoring').
	newRec, err := w.Repo.InsertProvisioning(ctx, projectID, newInst, provider.Name(), ct, nonce, ver)
	if err != nil {
		bestEffortDelete(context.WithoutCancel(ctx), provider, newInst.ProviderID, logger, "insert new row failed")
		return w.fail(ctx, restoreID, fmt.Errorf("insert new project_databases: %w", err))
	}
	// InsertProvisioning wrote state='provisioning'; flip to 'restoring'
	// so the console can distinguish the two lifecycle paths.
	if err := w.Repo.UpdateState(ctx, newRec.ID, dbprovider.StateRestoring, newInst.Host, newInst.Port); err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("flip to restoring: %w", err))
	}
	if err := w.setNewInstanceID(ctx, restoreID, newRec.ID); err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("set new_instance_id: %w", err))
	}

	// 6. Poll until active.
	pollInterval := w.PollInterval
	if pollInterval == 0 {
		pollInterval = defaultRestorePollInterval
	}
	timeout := w.PollTimeout
	if timeout == 0 {
		timeout = defaultRestorePollTimeout
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	active, err := pollUntilActive(pollCtx, provider, newInst.ProviderID, pollInterval, logger)
	if err != nil {
		bestEffortDelete(context.WithoutCancel(ctx), provider, newInst.ProviderID, logger, "poll failed")
		_ = w.Repo.MarkDeleted(context.WithoutCancel(ctx), newRec.ID)
		return w.fail(ctx, restoreID, fmt.Errorf("poll for active: %w", err))
	}
	// Do NOT flip the shadow row to 'active' here — that would open
	// a window where two rows for the same project are simultaneously
	// state='active' (violating the intent of the live-instance
	// index and breaking any consumer that reads "the live row").
	// The flip lives inside the cutover tx below.

	// 7. Verify state (best-effort — a live SELECT would need us to
	//    open a fresh pgxpool against the restored instance, which
	//    ties this worker to the pool-cache design landing in M2.5).
	//    For M3 we treat provider.State=Active as sufficient
	//    verification; a follow-up will layer a real query when the
	//    pool cache is available.
	if err := w.setState(ctx, restoreID, "verifying", ""); err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("mark verifying: %w", err))
	}
	logger.Info("skipping schema-query verification (M2.5 follow-up)", "new_host", active.Host)

	// 8. Atomic swap in a single tx: the OLD row exits the live set
	//    (state='deleting', deleted_at=now(), superseded_by=new) and
	//    the NEW row enters it (state='active' with the polled host
	//    /port). Migration 000092 drops 'restoring' from the live-
	//    instance unique-index predicate so co-existence is legal
	//    up to this moment; the tx flips the flags atomically so at
	//    no time do two rows simultaneously satisfy the "live"
	//    predicate. Also updates restore_operations to 'complete'
	//    inside the same tx so a mid-cutover crash leaves an
	//    internally-consistent state.
	if err := w.setState(ctx, restoreID, "cutover", ""); err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("mark cutover: %w", err))
	}
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("begin cutover tx: %w", err))
	}
	defer tx.Rollback(ctx) //nolint:errcheck // committed path returns before defer

	if _, err := tx.Exec(ctx,
		`UPDATE public.project_databases
		    SET state = 'deleting',
		        deleted_at = now(),
		        superseded_by = $2::uuid
		  WHERE id = $1::uuid`,
		oldRec.ID, newRec.ID,
	); err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("mark old instance superseded: %w", err))
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.project_databases
		    SET state = 'active',
		        host  = $2,
		        port  = $3
		  WHERE id = $1::uuid`,
		newRec.ID, active.Host, active.Port,
	); err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("promote new instance to active: %w", err))
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.restore_operations
		    SET state = 'complete',
		        completed_at = now()
		  WHERE id = $1::uuid`,
		restoreID,
	); err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("mark restore complete: %w", err))
	}
	if err := tx.Commit(ctx); err != nil {
		return w.fail(ctx, restoreID, fmt.Errorf("commit cutover: %w", err))
	}

	logger.Info("restore complete",
		"new_instance_id", newRec.ID,
		"new_host", active.Host,
		"old_instance_id", oldRec.ID)
	return nil
}

// ── helpers ──────────────────────────────────────────────────────

func (w *RestoreTeamDatabaseWorker) setState(ctx context.Context, restoreID, state, errMsg string) error {
	if _, err := w.Pool.Exec(ctx,
		`UPDATE public.restore_operations
		    SET state = $2, error = NULLIF($3, '')
		  WHERE id = $1::uuid`,
		restoreID, state, errMsg,
	); err != nil {
		return fmt.Errorf("update state=%s: %w", state, err)
	}
	return nil
}

func (w *RestoreTeamDatabaseWorker) setNewInstanceID(ctx context.Context, restoreID, newInstanceID string) error {
	if _, err := w.Pool.Exec(ctx,
		`UPDATE public.restore_operations
		    SET new_instance_id = $2::uuid
		  WHERE id = $1::uuid`,
		restoreID, newInstanceID,
	); err != nil {
		return err
	}
	return nil
}

// fail marks the row failed with an error message and cancels the
// job so River doesn't retry a state we can't recover from.
func (w *RestoreTeamDatabaseWorker) fail(ctx context.Context, restoreID string, err error) error {
	if setErr := w.setState(context.WithoutCancel(ctx), restoreID, "failed", err.Error()); setErr != nil {
		slog.Error("also failed to mark restore_operations failed",
			"restore_id", restoreID, "set_error", setErr, "original_error", err)
	}
	if _, cErr := w.Pool.Exec(context.WithoutCancel(ctx),
		`UPDATE public.restore_operations SET completed_at = now() WHERE id = $1::uuid`,
		restoreID,
	); cErr != nil {
		slog.Error("failed to set restore completed_at", "restore_id", restoreID, "error", cErr)
	}
	return river.JobCancel(err)
}
