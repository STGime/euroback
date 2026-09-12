package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/eurobase/euroback/internal/plans"
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
	// Limits resolves plan_limits for the project (used to derive
	// backup retention days for the SetBackupSchedule call). Nil-
	// safe — the schedule step is skipped with a warning if unset,
	// and the reconcile sweeper picks up the row on the next tick.
	// Same defensive posture as the bootstrap step's nil-cipher
	// handling.
	Limits *plans.LimitsService
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
	// River-path idempotency key = job.ID. Retries of the same job
	// share it; a re-enqueue after `MarkDeleted` produces a NEW job
	// ID, so Scaleway's 24 h idempotency cache doesn't fold the
	// second provision into the first (would otherwise return a
	// stale reference to a torn-down instance — the #560 review
	// regression).
	_, err := w.Ensure(ctx, job.Args, job.Attempt, fmt.Sprintf("provision-%d", job.ID))
	return err
}

// Ensure runs the full "get a live, active project_databases row"
// flow synchronously and returns the record when it reaches
// state='active' with the runtime credential bootstrapped. Callable
// from two places:
//
//  1. Work() — the River async path. Discards the return value; the
//     row lives in Postgres and the SDK routing cache picks it up on
//     the next request.
//  2. The upgrade orchestrator (PR 5 in team-tier/upgrade-path) —
//     needs the record ID so it can stamp
//     project_upgrades.project_database_id and route data-copy
//     traffic to the dedicated instance.
//
// `attempt` mirrors River's job.Attempt. Used only for the
// resume-from-active optimisation (see the comment inside): when >1
// AND a live active row already exists for the project, skip
// Provision + InsertProvisioning and re-run only the bootstrap step.
// Callers driving this synchronously outside of River can pass 1 for
// the first attempt.
//
// `idempotencyKey` is forwarded to provider.Provision(). Callers
// MUST provide a value that rotates per "generation" (project +
// deleted-and-re-provisioned counter) so Scaleway's idempotency
// cache doesn't return metadata for a torn-down instance. Work()
// uses `provision-{job.ID}` (River retries share it, re-enqueues
// don't). Synchronous callers should salt with something equally
// unique per upgrade attempt (e.g. `upgrade-{project_upgrades.id}`).
//
// Same idempotency guarantees as Work(): safe to call multiple times
// for the same project. First success returns the record; subsequent
// calls hit the resume-from-active branch (attempt >= 2).
func (w *ProvisionTeamDatabaseWorker) Ensure(ctx context.Context, args jobs.ProvisionTeamDatabaseArgs, attempt int, idempotencyKey string) (*dbprovider.Record, error) {
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
		return nil, river.JobCancel(err)
	}

	provider, err := w.Registry.Get(args.Provider)
	if err != nil {
		logger.Error("provider not registered", "error", err)
		// Config error — do not retry.
		return nil, river.JobCancel(err)
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
	if attempt > 1 {
		if existing, err := w.Repo.GetLiveByProject(ctx, args.ProjectID); err == nil && existing.State == dbprovider.StateActive {
			logger.Info("resuming from active row — re-running bootstrapRuntime only",
				"project_database_id", existing.ID,
				"host", existing.Host,
				"attempt", attempt)
			activeInst := &dbprovider.Instance{
				ProviderID: existing.ProviderInstanceID,
				Host:       existing.Host,
				Port:       existing.Port,
				DBName:     existing.DatabaseName,
				Username:   existing.Username,
				Region:     existing.Region,
				State:      existing.State,
			}
			if err := w.bootstrapRuntime(ctx, provider, existing, activeInst, logger); err != nil {
				return nil, fmt.Errorf("resume bootstrap dedicated: %w", err)
			}
			// Same rationale as the fresh-provision path: re-fetch so
			// runtime + readonly credential slots reflect what
			// bootstrapRuntime just wrote (existing was fetched
			// BEFORE bootstrap, so its slots are stale).
			fresh, refetchErr := w.Repo.GetLiveByProject(ctx, args.ProjectID)
			if refetchErr != nil {
				logger.Warn("final GetLiveByProject failed on resume path — returning pre-bootstrap rec (runtime slots may be stale)",
					"error", refetchErr)
				return existing, nil
			}
			return fresh, nil
		}
	}

	size := dbprovider.Size(args.Size)
	if size == "" {
		// SizeSmall = Scaleway db-dev-s (2 vCPU / 4 GB RAM) with a
		// 50 GB `sbs_5k` volume (see dbprovider/scaleway.go —
		// SizeSmall's volume is 50 GB, NOT 10 GB, to stay ≥ Team's
		// plan_limits.db_size_mb=100 GB… wait, 50 < 100 — see below).
		//
		// Right-sized for the €149/mo Team price point + the SMB
		// buyer profile (<5k signed-up users, <10 GB active DB, <100
		// req/s). Previous default (SizeMedium = db-gp-s, 4 vCPU /
		// 16 GB RAM, 50 GB) was ~€115-136/mo of Scaleway spend on a
		// €149/mo tier — near-zero gross margin once support + backup
		// storage + fixed platform costs land. Compute downsize is
		// the ~€60/mo win; storage delta 50→10 GB was only ~€4/mo
		// and would have collided with plan_limits.db_size_mb (see
		// the map comment). Keep compute down, keep storage at 50 GB.
		//
		// Note: 50 GB starter is still below the 100 GB db_size_mb
		// plan cap, so a customer approaching their cap still needs
		// an online volume resize (or a plan_limits bump). Follow-up
		// #366 makes volume_type + volume_size env-configurable so
		// ops can lift specific projects without a code change.
		//
		// HA note: Scaleway HA requires the gp compute tier. On
		// db-dev-s (starter), IsHaCluster is unavailable — a Team
		// customer who needs failover-on-primary-loss goes through
		// an online compute upgrade to db-gp-s first, THEN enables
		// HA. Marketing surfaces (DPA Annex 2, /security) must not
		// promise HA as a starter-shape capability.
		size = dbprovider.SizeSmall
	}

	// Idempotency-Key comes from the caller so River retries share it
	// (same job.ID) while re-enqueues after MarkDeleted get a fresh
	// value. See the Ensure doc comment for the contract.
	inst, err := provider.Provision(ctx, dbprovider.ProvisionOpts{
		ProjectID:      args.ProjectID,
		Slug:           args.Slug,
		Size:           size,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if isNonRetryable(err) {
			logger.Error("provider provisioning failed non-retryably", "error", err)
			return nil, river.JobCancel(err)
		}
		logger.Warn("provider provisioning failed — will retry", "error", err)
		return nil, fmt.Errorf("provision: %w", err)
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
		return nil, fmt.Errorf("seal password: %w", err)
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
		return nil, fmt.Errorf("insert project_databases: %w", err)
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
		return nil, fmt.Errorf("poll: %w", err)
	}

	if err := w.Repo.UpdateState(ctx, rec.ID, dbprovider.StateActive, active.Host, active.Port); err != nil {
		return nil, fmt.Errorf("mark active: %w", err)
	}
	// Keep the in-memory record in sync with the row we just wrote,
	// so Ensure's return value accurately reflects live state for
	// callers (e.g. the upgrade orchestrator reads Host/Port to
	// connect to the fresh instance for the data-copy step).
	rec.State = dbprovider.StateActive
	rec.Host = active.Host
	rec.Port = active.Port
	logger.Info("team-tier database ready", "host", active.Host, "port", active.Port)

	// Team-tier M3 follow-up (#457) — set the Scaleway backup schedule
	// so `autobackup_*` retention matches plan_limits.backup_retention_days
	// instead of Scaleway's undocumented default. Best-effort:
	//   - Nil Limits (dev/tests without a plans service) → skip; the
	//     sweeper picks it up once the service is wired in prod.
	//   - Zero retention (misconfigured plan) → log-loud and skip;
	//     the sweeper worker will JobCancel the same way if a stale
	//     row reaches it, so ops sees the config bug immediately.
	//   - Provider error (e.g. Scaleway warmup 503) → log-warn and
	//     continue; MarkBackupScheduleApplied is NOT called, so the
	//     reconcile sweeper enqueues a retry on its next tick. This
	//     is the specific self-heal the reviewer flagged as
	//     mandatory on #457.
	//
	// On success, MarkBackupScheduleApplied stamps the row so the
	// sweeper stops seeing it.
	w.applyBackupSchedule(ctx, provider, rec, logger)

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
	if err := w.bootstrapRuntime(ctx, provider, rec, active, logger); err != nil {
		// Loud error, but the row stays in state='active' — the
		// owner credential is usable; the runtime credential just
		// isn't populated yet. PoolCache's EffectiveCredential
		// fallback keeps SDK traffic on the shared pool (which is
		// where TEAM_TIER_ROUTING=0 keeps it anyway). River retries
		// this job; ops can also re-enqueue.
		logger.Error("bootstrap dedicated instance failed — runtime credential not populated; owner still usable",
			"error", err)
		return nil, fmt.Errorf("bootstrap dedicated: %w", err)
	}

	// Re-fetch the record so the returned value reflects EVERY field
	// bootstrapRuntime + applyBackupSchedule wrote (runtime creds,
	// readonly creds, backup_schedule_applied_at). Callers that need
	// runtime credentials to open a fresh pool (upgrade orchestrator
	// in PR 5) get them without a follow-up round-trip.
	//
	// Best-effort: if this fetch races a deletion, the in-memory rec
	// is still close-enough (state/host/port synced above) and the
	// caller can retry. Do NOT fail Ensure on refetch error — the
	// database is provably active and bootstrapped at this point.
	fresh, refetchErr := w.Repo.GetLiveByProject(ctx, args.ProjectID)
	if refetchErr != nil {
		logger.Warn("final GetLiveByProject failed after successful bootstrap — returning in-memory rec (runtime/readonly slots may be stale)",
			"error", refetchErr)
		return rec, nil
	}
	return fresh, nil
}

// bootstrapRuntime applies the dedicated-instance bootstrap SQL,
// creates the non-owner runtime role with a fresh password, calls
// provision_tenant, grants DB-level privileges via the provider's
// control plane (needed because Scaleway's `rdb` DB is owned by
// `_rdb_superadmin` — SQL GRANT CONNECT from eurobase_owner is a
// silent no-op), and writes the runtime credential into
// project_databases.runtime_*. Plaintext password lives only in
// this stack frame.
func (w *ProvisionTeamDatabaseWorker) bootstrapRuntime(
	ctx context.Context,
	provider dbprovider.Provider,
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

	// Grant DB-level privileges via the provider's control plane.
	// Required because Scaleway RDB's `rdb` database is owned by
	// `_rdb_superadmin`, not the customer-visible eurobase_owner —
	// SQL `GRANT CONNECT` from an eurobase_owner session is a
	// silent WARNING-not-error no-op. The provider's SetPrivilege
	// runs as its superadmin, bypassing the ownership limitation.
	// See dbprovider.PrivilegeGranter (provider.go) and Scaleway's
	// SetPrivilege (scaleway.go).
	//
	// Providers that don't implement PrivilegeGranter (e.g. a future
	// self-hosted provider where eurobase_owner really owns the DB)
	// skip this step — SQL grants in dedicated_bootstrap.sql cover
	// their case.
	if granter, ok := provider.(dbprovider.PrivilegeGranter); ok {
		for _, g := range []struct {
			user, perm string
		}{
			// Scaleway's `readwrite` = CRUD, no DDL — matches gateway.
			// Their `readonly` grants MORE than SELECT (verified against
			// myteam3), so we still call it to get CONNECT + baseline
			// grants, then LockdownReadonlyGrants below strips the
			// writes back off. Using `readonly` (rather than `readwrite`)
			// keeps the audit trail honest about intent.
			{creds.Runtime.Username, "readwrite"},
			{creds.Readonly.Username, "readonly"},
		} {
			if err := granter.SetPrivilege(ctx, rec.ProviderInstanceID, rec.DatabaseName, g.user, g.perm); err != nil {
				return fmt.Errorf("SetPrivilege(%s → %s): %w", g.user, g.perm, err)
			}
			logger.Info("provider-side DB privilege granted",
				"user", g.user, "permission", g.perm, "database", rec.DatabaseName)
		}
		// Post-grant lockdown: force eurobase_readonly back to
		// SELECT-only regardless of what the Scaleway `readonly`
		// permission actually granted. See LockdownReadonlyGrants
		// doc comment for the empirical justification.
		if err := dbprovider.LockdownReadonlyGrants(ctx, ownerDSN, schemaName, logger); err != nil {
			return fmt.Errorf("LockdownReadonlyGrants: %w", err)
		}
	} else {
		logger.Info("provider does not implement PrivilegeGranter — relying on SQL grants alone (safe for self-hosted / vanilla PG)")
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

// applyBackupSchedule is the inline provision-time call to Scaleway's
// set-backup-schedule endpoint (#457). All failure paths are
// non-fatal — the reconcile sweeper picks up any row where
// MarkBackupScheduleApplied didn't fire.
func (w *ProvisionTeamDatabaseWorker) applyBackupSchedule(
	ctx context.Context,
	provider dbprovider.Provider,
	rec *dbprovider.Record,
	logger *slog.Logger,
) {
	if w.Limits == nil {
		// Dev/test rig without a plans service. Sweeper handles it
		// in prod (where Limits is always wired).
		logger.Warn("backup schedule: skipped (plans service not configured; reconcile sweeper will handle)")
		return
	}
	limits, err := w.Limits.GetProjectLimits(ctx, rec.ProjectID)
	if err != nil {
		logger.Warn("backup schedule: plan lookup failed at provision time — leaving to reconcile sweeper",
			"error", err)
		return
	}
	if limits.BackupRetentionDays <= 0 {
		// Config bug — the plan itself is misconfigured. Loud but
		// non-fatal. The sweeper's worker will JobCancel a stale row
		// the same way, so ops sees this in slog + River UI.
		logger.Error("backup schedule: refusing to configure with zero retention — plan_limits.backup_retention_days must be > 0 for a dedicated-DB plan",
			"plan", limits.Plan,
			"backup_retention_days", limits.BackupRetentionDays)
		return
	}
	opts := dbprovider.SetBackupScheduleOpts{
		FrequencyHours: backupScheduleFrequencyHours,
		RetentionDays:  limits.BackupRetentionDays,
	}
	if err := provider.SetBackupSchedule(ctx, rec.ProviderInstanceID, opts); err != nil {
		// The reviewer's specific concern: a Scaleway warmup rejection
		// here would previously (without a sweeper) leave the instance
		// permanently on provider defaults. Now: log-warn and continue;
		// the reconcile sweeper enqueues a retry on its next tick,
		// which drains through the warmup window via River backoff.
		logger.Warn("backup schedule: inline SetBackupSchedule failed — reconcile sweeper will retry",
			"error", err)
		return
	}
	if err := w.Repo.MarkBackupScheduleApplied(ctx, rec.ID); err != nil {
		// Scaleway is correctly configured; local bookkeeping failed.
		// Sweeper will re-enqueue and re-apply idempotently.
		logger.Warn("backup schedule: MarkBackupScheduleApplied failed — reconcile sweeper will retry",
			"error", err)
		return
	}
	logger.Info("backup schedule applied at provision time",
		"retention_days", limits.BackupRetentionDays,
		"frequency_hours", backupScheduleFrequencyHours)
}
