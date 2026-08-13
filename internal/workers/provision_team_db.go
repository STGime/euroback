package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/riverqueue/river"
)

// ProvisionTeamDatabaseWorker provisions a dedicated managed-PG
// instance for a Team-tier project. Enqueued from the tenant service
// (M2) after the shared-cluster tenant schema is set up.
//
// Flow:
//   1. Look up the provider from the registry (fail-fast if unknown).
//   2. Provider.Provision — returns as soon as the provider assigns
//      an ID; the instance is still spinning up.
//   3. Seal the returned password with the platform cipher.
//   4. Insert project_databases row with state='provisioning'.
//   5. Poll Provider.Describe until StateActive or timeout.
//   6. Flip state='active' with the current host/port.
//   7. On terminal failure at any step: mark state='failed' and
//      return the error so River's retry logic can decide whether
//      to try again.
type ProvisionTeamDatabaseWorker struct {
	river.WorkerDefaults[jobs.ProvisionTeamDatabaseArgs]
	Registry *dbprovider.Registry
	Cipher   *dbprovider.Cipher
	Repo     *dbprovider.Repo
	// RuntimePasswordSecret is the shared secret used to derive the
	// per-instance eurobase_gateway login password
	// (HMAC-SHA256(secret, project_database_id) → hex64). Sourced
	// from RUNTIME_PASSWORD_SECRET in eurobase-secrets. Nil/empty
	// disables the bootstrap step — provisioning still succeeds up
	// to state=active, but the runtime credential slot stays NULL
	// and SDK routing falls back to shared (safe posture until the
	// secret is configured).
	RuntimePasswordSecret []byte
	// PollInterval is the delay between Describe() checks during the
	// active-wait loop. Defaults to 10s if zero — Scaleway RDB
	// typically reaches 'ready' within 90s to 4 minutes.
	PollInterval time.Duration
	// PollTimeout bounds the total wait for StateActive. Defaults
	// to 10 minutes if zero.
	PollTimeout time.Duration
}

const (
	defaultPollInterval = 10 * time.Second
	defaultPollTimeout  = 10 * time.Minute
	// provisionSlack is the wall-clock budget the worker needs
	// AROUND pollUntilActive: Provision + InsertProvisioning +
	// bootstrapRuntime (which opens a fresh psql connection under
	// its own 30s inner deadline and applies embedded SQL). 5 min
	// leaves ample headroom for the empirical <30 s bootstrap while
	// staying well under a MaxAttempts=5 × Timeout() total burn.
	//
	// The invariant `Timeout() >= defaultPollTimeout + provisionSlack`
	// is enforced by TestProvisionTeamDatabaseWorker_Timeout — a
	// future refactor that raises defaultPollTimeout without touching
	// slack (or vice versa) fails the test rather than silently
	// re-introducing the bug fixed here.
	provisionSlack = 5 * time.Minute
)

// Timeout overrides River's 1-minute default. pollUntilActive alone
// can wait up to defaultPollTimeout (10 min) for Scaleway RDB to
// reach `ready` (typically 90 s – 4 min), so the whole Work()
// invocation needs a ceiling that fully contains the poll plus the
// surrounding work.
//
// Without this override every attempt died at 60 s inside
// pollUntilActive → best-effort delete → MarkDeleted → River retry,
// burning MaxAttempts=5 in ~5 minutes without ever giving one
// instance time to reach `ready`. Root cause of the myteam project
// provisioning loop that never converged.
func (w *ProvisionTeamDatabaseWorker) Timeout(*river.Job[jobs.ProvisionTeamDatabaseArgs]) time.Duration {
	return defaultPollTimeout + provisionSlack
}

func (w *ProvisionTeamDatabaseWorker) Work(ctx context.Context, job *river.Job[jobs.ProvisionTeamDatabaseArgs]) error {
	args := job.Args
	logger := slog.With(
		"project_id", args.ProjectID,
		"slug", args.Slug,
		"provider", args.Provider,
		"region", args.Region,
	)
	logger.Info("provisioning team-tier database")

	// Fail fast on missing cipher — dev mode is allowed to boot the
	// worker without VAULT_ENCRYPTION_KEY (per cmd/worker/main.go),
	// but a Team-tier job that reaches this point can't proceed
	// without the ability to seal credentials.
	if w.Cipher == nil {
		err := errors.New("cipher not configured (VAULT_ENCRYPTION_KEY missing) — cannot seal Team-tier DB credentials")
		logger.Error(err.Error())
		return river.JobCancel(err)
	}

	provider, err := w.Registry.Get(args.Provider)
	if err != nil {
		logger.Error("provider not registered", "error", err)
		// Config error — do not retry.
		return river.JobCancel(err)
	}

	// Resume-from-active on retries only: if a previous attempt
	// reached state='active' and only the bootstrap step failed,
	// don't re-run Provision + InsertProvisioning (they'd insert
	// a SECOND project_databases row, which then violates the
	// `state='active'` partial unique index on the promote step
	// and stalls the whole retry chain with orphan `provisioning`
	// -state rows). Instead pick up the existing live row and
	// jump straight to bootstrapRuntime.
	//
	// Gated on job.Attempt > 1 because first attempts by definition
	// have no prior row — skipping the lookup keeps the happy path
	// clean and lets unit tests exercise the fresh-provision flow
	// without a live platform pool. On a River retry (Attempt >= 2)
	// the platform pool is always populated in production.
	if job.Attempt > 1 {
		if existing, err := w.Repo.GetLiveByProject(ctx, args.ProjectID); err == nil && existing.State == dbprovider.StateActive {
			logger.Info("resuming from active row — re-running bootstrapRuntime only",
				"project_database_id", existing.ID,
				"host", existing.Host,
				"attempt", job.Attempt)
			activeInst := &dbprovider.Instance{
				ProviderID: existing.ProviderInstanceID,
				Host:       existing.Host,
				Port:       existing.Port,
				DBName:     existing.DatabaseName,
				Username:   existing.Username,
				Region:     existing.Region,
				State:      existing.State,
			}
			if err := w.bootstrapRuntime(ctx, existing, activeInst, logger); err != nil {
				return fmt.Errorf("resume bootstrap dedicated: %w", err)
			}
			return nil
		}
	}

	size := dbprovider.Size(args.Size)
	if size == "" {
		size = dbprovider.SizeMedium
	}

	// Idempotency-Key = deterministic per job. River retries hit
	// the same key so Scaleway returns the previously-created
	// instance rather than spinning up a duplicate (~€50-500/mo of
	// orphan spend per leak on a MaxAttempts=5 job).
	idemKey := fmt.Sprintf("provision-%d", job.ID)

	inst, err := provider.Provision(ctx, dbprovider.ProvisionOpts{
		ProjectID:      args.ProjectID,
		Slug:           args.Slug,
		Size:           size,
		IdempotencyKey: idemKey,
	})
	if err != nil {
		if isNonRetryable(err) {
			logger.Error("provider provisioning failed non-retryably", "error", err)
			return river.JobCancel(err)
		}
		logger.Warn("provider provisioning failed — will retry", "error", err)
		return fmt.Errorf("provision: %w", err)
	}

	// Seal the password before writing to DB. The plaintext lives
	// only in memory during this worker invocation.
	ct, nonce, ver, err := w.Cipher.Seal(inst.Password)
	if err != nil {
		// Provider instance now exists but we can't seal its
		// credentials — best-effort cleanup so retries don't leak.
		// context.WithoutCancel because the parent ctx may already
		// be canceled by River's job timeout — a cancelled ctx makes
		// provider.Delete short-circuit on ctx.Err() and orphans the
		// paid instance (bug_003 from PR #331 review).
		bestEffortDelete(context.WithoutCancel(ctx), provider, inst.ProviderID, logger, "seal password failed")
		return fmt.Errorf("seal password: %w", err)
	}
	// Wipe plaintext to reduce accidental exposure via slog / panic.
	inst.Password = ""

	rec, err := w.Repo.InsertProvisioning(ctx, args.ProjectID, inst, provider.Name(), ct, nonce, ver)
	if err != nil {
		// Same story — provider instance exists but no local record.
		// Retries WILL hit Scaleway's idempotency cache (same job.ID
		// → same instance), so this cleanup is belt-and-suspenders.
		// But: the idempotency window on Scaleway may expire before
		// the last River retry, AND context.WithoutCancel is required
		// so the delete lands even if River is shutting down the pod
		// (bug_003 from PR #331 review).
		bestEffortDelete(context.WithoutCancel(ctx), provider, inst.ProviderID, logger, "insert row failed")
		return fmt.Errorf("insert project_databases: %w", err)
	}
	logger = logger.With("project_database_id", rec.ID, "provider_instance_id", inst.ProviderID)
	logger.Info("provider provisioning kicked off — polling for active")

	interval := w.PollInterval
	if interval == 0 {
		interval = defaultPollInterval
	}
	timeout := w.PollTimeout
	if timeout == 0 {
		timeout = defaultPollTimeout
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	active, err := pollUntilActive(pollCtx, provider, inst.ProviderID, interval, logger)
	if err != nil {
		logger.Error("poll for active state failed", "error", err)
		// Tear down the provider-side instance and mark the row
		// deleted_at=now() so the deprovision sweeper picks it up.
		// (MarkFailed alone would leave a `failed`-state row invisible
		// to the sweeper — `state='failed'` isn't in its filter, but
		// deleted_at IS NULL is — so the instance would keep billing
		// with no reclaim path.) The next River attempt hits Scaleway
		// with the same idempotency key, but at that point the
		// instance is gone so Scaleway allocates a fresh one — that's
		// the correct behaviour for a retry that follows a real failure.
		bestEffortDelete(context.WithoutCancel(ctx), provider, inst.ProviderID, logger, "poll timeout")
		if markErr := w.Repo.MarkDeleted(context.WithoutCancel(ctx), rec.ID); markErr != nil {
			logger.Error("failed to mark row deleted after poll failure", "mark_error", markErr)
		}
		return fmt.Errorf("poll: %w", err)
	}

	if err := w.Repo.UpdateState(ctx, rec.ID, dbprovider.StateActive, active.Host, active.Port); err != nil {
		return fmt.Errorf("mark active: %w", err)
	}
	logger.Info("team-tier database ready", "host", active.Host, "port", active.Port)

	// Team-tier M2.5 part 2b — bootstrap the fresh instance so it
	// can safely serve SDK traffic as a non-owner runtime role.
	// See internal/dbprovider/bootstrap.go for the flow.
	//
	// The instance is fully functional without this step (the M4
	// Direct Connection UI still works, since it hands out the
	// owner credentials). Bootstrap failure marks the row failed
	// so ops can retry; it does NOT tear the instance down (paid
	// resource; retry-friendly).
	//
	// Idempotent: BootstrapDedicated is safe to re-run on the same
	// instance for the same project.
	if err := w.bootstrapRuntime(ctx, rec, active, logger); err != nil {
		// Loud error, but the row stays in state='active' — the
		// owner credential is usable; the runtime credential just
		// isn't populated yet. PoolCache's EffectiveCredential
		// fallback keeps SDK traffic on the shared pool (which is
		// where TEAM_TIER_ROUTING=0 keeps it anyway). River retries
		// this job; ops can also re-enqueue.
		logger.Error("bootstrap dedicated instance failed — runtime credential not populated; owner still usable",
			"error", err)
		return fmt.Errorf("bootstrap dedicated: %w", err)
	}

	return nil
}

// bootstrapRuntime applies the dedicated-instance bootstrap SQL,
// creates the non-owner runtime role with a fresh password, calls
// provision_tenant, and writes the runtime credential into
// project_databases.runtime_*. Plaintext password lives only in
// this stack frame.
func (w *ProvisionTeamDatabaseWorker) bootstrapRuntime(
	ctx context.Context,
	rec *dbprovider.Record,
	active *dbprovider.Instance,
	logger *slog.Logger,
) error {
	// Owner DSN — reconstitute from the record + the sealed owner
	// password just committed in InsertProvisioning. Same shape as
	// PoolCache.buildDSN + connection_handlers.buildPostgresURL:
	// sslmode=require, URL-encoded credentials.
	ownerPassword, err := w.Cipher.Open(rec.PasswordCiphertext, rec.PasswordNonce, rec.PasswordKeyVersion)
	if err != nil {
		return fmt.Errorf("open owner password: %w", err)
	}
	ownerDSN := dbprovider.BuildOwnerDSN(rec.Username, ownerPassword, active.Host, active.Port, rec.DatabaseName)

	// Look up the human-readable project name so provision_tenant
	// can log it and (in the future) name related resources.
	// Optional: falls back to the project ID if the lookup errors,
	// so a transient projects-table hiccup doesn't fail bootstrap.
	displayName := rec.ProjectID
	// (The projects lookup lives outside the dbprovider package;
	//  we don't want to reach across the layer just for a log
	//  string. project_id in display works fine.)

	if len(w.RuntimePasswordSecret) == 0 {
		// Fail-safe: without the secret we can't derive the same
		// password on retries → future ALTER ROLE would drift.
		// Skip the bootstrap step entirely and leave the runtime
		// slot NULL. The instance stays fully usable via the owner
		// credential (M4 Direct Connection). Backfill sweeper picks
		// it up once the secret is configured.
		logger.Warn("RUNTIME_PASSWORD_SECRET not set — skipping bootstrap; runtime credential slot will remain NULL")
		return nil
	}
	runtimePassword := dbprovider.DeriveRuntimePassword(w.RuntimePasswordSecret, rec.ID)
	readonlyPassword := dbprovider.DeriveReadonlyPassword(w.RuntimePasswordSecret, rec.ID)

	creds, schemaName, err := dbprovider.BootstrapDedicated(ctx, ownerDSN, rec.ProjectID, displayName, runtimePassword, readonlyPassword, logger)
	if err != nil {
		return fmt.Errorf("BootstrapDedicated: %w", err)
	}

	// Seal + persist the runtime credential.
	runtimeCT, runtimeNonce, runtimeVer, err := w.Cipher.Seal(creds.Runtime.Password)
	if err != nil {
		return fmt.Errorf("seal runtime password: %w", err)
	}
	won, err := w.Repo.SetRuntimeCredentials(ctx, rec.ID, creds.Runtime.Username, runtimeCT, runtimeNonce, runtimeVer)
	if err != nil {
		return fmt.Errorf("persist runtime credentials: %w", err)
	}
	if !won {
		// A concurrent bootstrap runner (backfill sweeper firing
		// while this provisioning attempt was in flight) already
		// populated the slot. Our ALTER ROLE on Scaleway is wasted
		// work but not harmful — the winner also rotated the same
		// role. Log and exit cleanly.
		//
		// Note: we still try to persist the readonly credential
		// below because 000101 was added AFTER 000093, so a project
		// whose runtime slot was populated by a pre-000101 runner
		// may still have a NULL readonly slot that our concurrent-
		// friendly write can safely fill.
		logger.Info("runtime credential already populated by a concurrent runner — skipping runtime write")
	} else {
		logger.Info("runtime credential populated — SDK traffic can now route as non-owner",
			"runtime_username", creds.Runtime.Username,
			"schema", schemaName)
	}

	// Seal + persist the readonly credential. Same only-if-NULL
	// concurrency contract as the runtime write (SetReadonly-
	// Credentials returns won=false if a concurrent runner won).
	roCT, roNonce, roVer, err := w.Cipher.Seal(creds.Readonly.Password)
	if err != nil {
		return fmt.Errorf("seal readonly password: %w", err)
	}
	roWon, err := w.Repo.SetReadonlyCredentials(ctx, rec.ID, creds.Readonly.Username, roCT, roNonce, roVer)
	if err != nil {
		return fmt.Errorf("persist readonly credentials: %w", err)
	}
	if !roWon {
		logger.Info("readonly credential already populated by a concurrent runner — skipping readonly write")
	} else {
		logger.Info("readonly credential populated — /connection?role=readonly can now hand out non-owner DSN",
			"readonly_username", creds.Readonly.Username,
			"schema", schemaName)
	}
	return nil
}

// pollUntilActive polls Describe at the configured interval until
// StateActive, StateFailed, or the context expires. Returns the
// Instance at the moment of the state transition.
//
// Non-retryable errors from Describe (auth failure, unknown
// provider, malformed request) exit the loop immediately rather
// than burning the full PollTimeout — a rotated SCW_SECRET_KEY
// mid-provision should surface in seconds, not 10 minutes.
func pollUntilActive(
	ctx context.Context,
	provider dbprovider.Provider,
	instanceID string,
	interval time.Duration,
	logger *slog.Logger,
) (*dbprovider.Instance, error) {
	for {
		inst, err := provider.Describe(ctx, instanceID)
		if err != nil {
			if isNonRetryable(err) {
				return nil, err
			}
			// Transient errors during startup are common — log +
			// retry rather than fail the whole worker.
			logger.Warn("describe errored during polling", "error", err)
		} else {
			switch inst.State {
			case dbprovider.StateActive:
				if inst.Host == "" || inst.Port == 0 {
					// Provider reports active but endpoint not yet
					// visible — treat as still-provisioning.
					logger.Warn("state=active but endpoint empty; continuing to poll")
				} else {
					return inst, nil
				}
			case dbprovider.StateFailed:
				return nil, errors.New("provider reports failed state")
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// bestEffortDelete tears down a just-provisioned instance when a
// downstream step (seal, insert, poll) fails. Uses the un-cancelled
// context so the delete lands even if the parent job is being
// aborted by River. Logs but never returns errors — the caller has
// already decided the job outcome.
func bestEffortDelete(
	ctx context.Context,
	provider dbprovider.Provider,
	instanceID string,
	logger *slog.Logger,
	reason string,
) {
	if err := provider.Delete(ctx, instanceID); err != nil {
		logger.Error("best-effort delete after failed provisioning also failed — instance may be orphaned",
			"instance_id", instanceID,
			"reason", reason,
			"delete_error", err,
		)
		return
	}
	logger.Info("best-effort delete after failed provisioning succeeded",
		"instance_id", instanceID,
		"reason", reason,
	)
}

// isNonRetryable returns true for errors that will never succeed on
// retry — auth failures, unknown provider, malformed request.
func isNonRetryable(err error) bool {
	return errors.Is(err, dbprovider.ErrUnauthorized) ||
		errors.Is(err, dbprovider.ErrInvalidRequest) ||
		errors.Is(err, dbprovider.ErrProviderNotRegistered)
}
