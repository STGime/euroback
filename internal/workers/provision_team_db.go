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

	inst, err := provider.Provision(ctx, dbprovider.ProvisionOpts{
		ProjectID: args.ProjectID,
		Slug:      args.Slug,
		Region:    args.Region,
		Size:      size,
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
		return fmt.Errorf("seal password: %w", err)
	}
	// Wipe plaintext to reduce accidental exposure via slog / panic.
	inst.Password = ""

	rec, err := w.Repo.InsertProvisioning(ctx, args.ProjectID, inst, provider.Name(), ct, nonce, ver)
	if err != nil {
		// The provider instance now exists but we can't record it
		// — worst-case orphan. Return retryable; on repeated failure
		// operations should reconcile manually.
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
		if markErr := w.Repo.MarkFailed(context.WithoutCancel(ctx), rec.ID); markErr != nil {
			logger.Error("also failed to mark row as failed", "mark_error", markErr)
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

// isNonRetryable returns true for errors that will never succeed on
// retry — auth failures, unknown provider, malformed request.
func isNonRetryable(err error) bool {
	return errors.Is(err, dbprovider.ErrUnauthorized) ||
		errors.Is(err, dbprovider.ErrInvalidRequest) ||
		errors.Is(err, dbprovider.ErrProviderNotRegistered)
}
