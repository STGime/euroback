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
	Cipher   *dbprovider.Cipher
	Repo     *dbprovider.Repo
	Registry *dbprovider.Registry
	// RuntimePasswordSecret — same value as the provision worker's
	// field. Both derive `HMAC(secret, project_database_id)` so the
	// eurobase_gateway password is identical no matter which runner
	// (provision retry, this backfill, a future rotate) sets it.
	RuntimePasswordSecret []byte
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
	if len(w.RuntimePasswordSecret) == 0 {
		// Same reason: without the shared secret the password we
		// derive would be different from what other runners
		// derived (or would derive on retry) → Scaleway/DB drift.
		// Config error, do not retry.
		return river.JobCancel(errors.New("RUNTIME_PASSWORD_SECRET not configured — cannot derive stable runtime password"))
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
	if rec.RuntimeUsername != nil && rec.ReadonlyUsername != nil {
		logger.Info("skipping backfill — both runtime and readonly credentials already populated")
		return nil
	}

	// Reconstitute the owner DSN + call BootstrapDedicated. Same code
	// path the provision worker uses — the bootstrap SQL is
	// idempotent so a project whose owner-side state is fine (schema
	// exists, tenant tables in place, RLS policies set) just gets a
	// fresh runtime + readonly password. Anything the previous
	// provision worker (M1/M2 era, before Part 2b/PR-B) couldn't do
	// gets done here.
	ownerPassword, err := w.Cipher.Open(rec.PasswordCiphertext, rec.PasswordNonce, rec.PasswordKeyVersion)
	if err != nil {
		return fmt.Errorf("open owner password: %w", err)
	}
	ownerDSN := dbprovider.BuildOwnerDSN(rec.Username, ownerPassword, rec.Host, rec.Port, rec.DatabaseName)

	runtimePassword := dbprovider.DeriveRuntimePassword(w.RuntimePasswordSecret, rec.ID)
	readonlyPassword := dbprovider.DeriveReadonlyPassword(w.RuntimePasswordSecret, rec.ID)
	creds, schemaName, err := dbprovider.BootstrapDedicated(ctx, ownerDSN, rec.ProjectID, rec.ProjectID, runtimePassword, readonlyPassword, logger)
	if err != nil {
		return fmt.Errorf("BootstrapDedicated: %w", err)
	}

	// Grant DB-level privileges via the provider's control plane —
	// same rationale as provision_team_db.go's identical block. Needed
	// so backfilling a pre-privilege-grant instance actually fixes it
	// (SQL grants alone don't take on Scaleway's `rdb` because
	// eurobase_owner isn't the DB owner). Registry lookup is best-
	// effort here: an older worker binary without Registry wired
	// would skip the grant with a warning and rely on SQL-only,
	// which is safe on self-hosted / vanilla-PG.
	if w.Registry != nil {
		if provider, gerr := w.Registry.Get(rec.Provider); gerr == nil {
			if granter, ok := provider.(dbprovider.PrivilegeGranter); ok {
				for _, g := range []struct {
					user, perm string
				}{
					{creds.Runtime.Username, "readwrite"},
					{creds.Readonly.Username, "readonly"},
				} {
					if err := granter.SetPrivilege(ctx, rec.ProviderInstanceID, rec.DatabaseName, g.user, g.perm); err != nil {
						return fmt.Errorf("SetPrivilege(%s → %s): %w", g.user, g.perm, err)
					}
					logger.Info("provider-side DB privilege granted",
						"user", g.user, "permission", g.perm)
				}
				// Lock readonly down to SELECT-only after the API
				// grant (Scaleway's `readonly` grants more than
				// SELECT — same rationale as provision_team_db.go).
				if err := dbprovider.LockdownReadonlyGrants(ctx, ownerDSN, schemaName, logger); err != nil {
					return fmt.Errorf("LockdownReadonlyGrants: %w", err)
				}
			}
		} else {
			logger.Warn("registry lookup failed — skipping provider-side privilege grant", "provider", rec.Provider, "error", gerr)
		}
	} else {
		logger.Warn("backfill worker has no Registry wired — skipping provider-side privilege grant (SQL grants only)")
	}

	// Persist whichever slot(s) are still NULL. SetRuntimeCredentials /
	// SetReadonlyCredentials both CAS on "column IS NULL", so filling
	// only one of them (the other having been populated by a prior
	// runner) is a no-op the CAS gracefully swallows.
	runtimeCT, runtimeNonce, runtimeVer, err := w.Cipher.Seal(creds.Runtime.Password)
	if err != nil {
		return fmt.Errorf("seal runtime password: %w", err)
	}
	runtimeWon, err := w.Repo.SetRuntimeCredentials(ctx, rec.ID, creds.Runtime.Username, runtimeCT, runtimeNonce, runtimeVer)
	if err != nil {
		return fmt.Errorf("persist runtime credentials: %w", err)
	}
	if !runtimeWon {
		logger.Info("backfill: runtime credential already populated (concurrent runner)")
	} else {
		logger.Info("backfill: runtime credential populated",
			"runtime_username", creds.Runtime.Username,
			"schema", schemaName)
	}

	roCT, roNonce, roVer, err := w.Cipher.Seal(creds.Readonly.Password)
	if err != nil {
		return fmt.Errorf("seal readonly password: %w", err)
	}
	roWon, err := w.Repo.SetReadonlyCredentials(ctx, rec.ID, creds.Readonly.Username, roCT, roNonce, roVer)
	if err != nil {
		return fmt.Errorf("persist readonly credentials: %w", err)
	}
	if !roWon {
		logger.Info("backfill: readonly credential already populated (concurrent runner)")
	} else {
		logger.Info("backfill: readonly credential populated",
			"readonly_username", creds.Readonly.Username,
			"schema", schemaName)
	}
	return nil
}
