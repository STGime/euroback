package workers

// BackfillRuntimeCredentialWorker runs BootstrapDedicated against a
// Team-tier project that was provisioned BEFORE M2.5 part 2b
// (PR #340) landed and therefore has no non-owner runtime credential
// in project_databases.runtime_*.
//
// Without this backfill, an existing Team project would stay on the
// shared pool after TEAM_TIER_ROUTING flips: PoolCache.Effective-
// Credential falls back to the owner credential when runtime is
// NULL, but the resolver treats "owner credential" the same as "no
// dedicated pool" → falls back to shared cluster → the whole
// dedicated instance sits idle.
//
// The worker is intentionally single-project: one job per row. A
// separate periodic sweeper (BackfillSweepWorker below) reads the
// list of pre-part-2b rows once per hour and fans out these jobs.
// This gives River the retry semantics + observability we want, and
// keeps each unit of work small (one BootstrapDedicated call ~= a
// few seconds).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type BackfillRuntimeCredentialWorker struct {
	river.WorkerDefaults[jobs.BackfillRuntimeCredentialArgs]
	Cipher *dbprovider.Cipher
	Repo   *dbprovider.Repo
}

func (w *BackfillRuntimeCredentialWorker) Work(ctx context.Context, job *river.Job[jobs.BackfillRuntimeCredentialArgs]) error {
	logger := slog.With(
		"project_database_id", job.Args.ProjectDatabaseID,
		"attempt", job.Attempt,
	)

	if w.Cipher == nil {
		// Same posture as ProvisionTeamDatabaseWorker — dev mode can
		// boot the worker without VAULT_ENCRYPTION_KEY but a real
		// backfill needs to open the owner password.
		return river.JobCancel(errors.New("cipher not configured — cannot open sealed owner password"))
	}

	rec, err := w.Repo.Get(ctx, job.Args.ProjectDatabaseID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Row was deleted between sweeper enqueue and worker
			// pickup — a Team project cancelled mid-backfill. Cancel
			// the job rather than retry.
			return river.JobCancel(fmt.Errorf("project_databases row gone: %w", err))
		}
		return fmt.Errorf("load project_databases row: %w", err)
	}

	// Skip guards — the sweeper's SQL filter should already exclude
	// these cases, but a race between the sweeper query and the
	// worker pickup (a Provision worker retry populating runtime in
	// between) is possible. Idempotent fast-paths avoid useless work.
	if rec.State != dbprovider.StateActive {
		logger.Info("skipping backfill — instance is not active", "state", rec.State)
		return nil
	}
	if rec.RuntimeUsername != nil {
		logger.Info("skipping backfill — runtime credential already populated")
		return nil
	}

	// Reconstitute the owner DSN + call BootstrapDedicated. Same code
	// path the provision worker uses — the bootstrap SQL is
	// idempotent so a project whose owner-side state is fine (schema
	// exists, tenant tables in place, RLS policies set) just gets a
	// fresh runtime password. Anything the previous provision worker
	// (M1/M2 era, before Part 2b) couldn't do gets done here.
	ownerPassword, err := w.Cipher.Open(rec.PasswordCiphertext, rec.PasswordNonce, rec.PasswordKeyVersion)
	if err != nil {
		return fmt.Errorf("open owner password: %w", err)
	}
	ownerDSN := dbprovider.BuildOwnerDSN(rec.Username, ownerPassword, rec.Host, rec.Port, rec.DatabaseName)

	cred, schemaName, err := dbprovider.BootstrapDedicated(ctx, ownerDSN, rec.ProjectID, rec.ProjectID, logger)
	if err != nil {
		return fmt.Errorf("BootstrapDedicated: %w", err)
	}

	ct, nonce, ver, err := w.Cipher.Seal(cred.Password)
	if err != nil {
		return fmt.Errorf("seal runtime password: %w", err)
	}
	won, err := w.Repo.SetRuntimeCredentials(ctx, rec.ID, cred.Username, ct, nonce, ver)
	if err != nil {
		return fmt.Errorf("persist runtime credentials: %w", err)
	}
	if !won {
		// A concurrent runner (provision-worker resume path, or an
		// earlier sweeper tick still finishing) already populated
		// the slot. Our ALTER ROLE on Scaleway is wasted work but
		// not harmful — Scaleway ended up with the winner's
		// password too. Log and exit cleanly.
		logger.Info("backfill lost the race — runtime credential already populated by a concurrent runner")
		return nil
	}

	logger.Info("backfill complete — runtime credential populated",
		"runtime_username", cred.Username,
		"schema", schemaName)
	return nil
}
