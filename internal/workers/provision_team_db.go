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
)

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
		// be canceled by River's job timeout.
		bestEffortDelete(ctx, provider, inst.ProviderID, logger, "seal password failed")
		return fmt.Errorf("seal password: %w", err)
	}
	// Wipe plaintext to reduce accidental exposure via slog / panic.
	inst.Password = ""

	rec, err := w.Repo.InsertProvisioning(ctx, args.ProjectID, inst, provider.Name(), ct, nonce, ver)
	if err != nil {
		// Same story — provider instance exists but no local
		// record. Retries WILL now hit Scaleway's idempotency
		// cache (same job.ID → same instance), so this cleanup
		// is belt-and-suspenders. Belt only, actually — the
		// idempotency window on Scaleway may expire before the
		// last River retry.
		bestEffortDelete(ctx, provider, inst.ProviderID, logger, "insert row failed")
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
