package workers

// Team-tier M2.5 part 2b — backfill sweeper.
//
// Once-per-hour tick that walks project_databases WHERE
// runtime_username IS NULL AND state='active' and fans out one
// BackfillRuntimeCredentialArgs per matching row. Each row gets
// its own River job so per-project failures are visible in the
// River UI and retryable independently.
//
// Bounded batch size (25 per tick, matches Repo.ListActiveWithoutRuntime
// default) — protects Scaleway from a thundering herd of concurrent
// bootstrap connections. At an hourly cadence + 25/tick, a
// hypothetical 500-project backlog drains in ~20 hours (fine for a
// one-off historical fix that runs after part 2b lands).
//
// Idempotent: once a project's runtime slot is populated, the WHERE
// filter excludes it — the sweeper naturally stops firing jobs for
// completed projects. Duplicate concurrent enqueues are collapsed
// by river.UniqueOpts on BackfillRuntimeCredentialArgs, and a
// losing concurrent write to project_databases returns won=false
// (see Repo.SetRuntimeCredentials) so at most one worker actually
// persists.
//
// ⚠️  Backfilling a runtime credential DOES NOT MOVE TENANT DATA.
// A project provisioned before part 2b has its tenant data on the
// SHARED cluster (routing was off then). BootstrapDedicated
// provisions a *fresh, empty* tenant schema on the dedicated
// instance. Flipping TEAM_TIER_ROUTING=1 for such a project would
// route SDK traffic to an empty schema — the tenant's data would
// appear gone.
//
// The sweeper's job is to close the credential/routing gap only.
// The data-cutover story (dump from shared, restore into dedicated,
// then flip the flag) is a separate ops procedure per project.
// See #338 for the checklist.

import (
	"context"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const backfillSweeperInterval = 1 * time.Hour
const backfillBatchSize = 25

// StartBackfillSweeper launches the once-per-hour sweeper. Returns
// immediately; loop exits when ctx is cancelled.
//
// The initial run is delayed by one interval rather than fired
// immediately — right at pod-start the River client is still
// warming up, and existing pre-part-2b projects have already lived
// without a runtime credential for weeks, so a one-hour delay
// costs nothing and avoids a startup-race edge case.
func StartBackfillSweeper(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) {
	go func() {
		t := time.NewTicker(backfillSweeperInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runBackfillSweeper(ctx, pool, riverClient); err != nil {
					slog.Error("backfill sweeper: run failed", "error", err)
				}
			}
		}
	}()
	slog.Info("backfill sweeper started", "interval", backfillSweeperInterval, "batch", backfillBatchSize)
}

func runBackfillSweeper(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) error {
	repo := dbprovider.NewRepo(pool)
	rows, err := repo.ListActiveWithoutRuntime(ctx, backfillBatchSize)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		// No work — silent. Avoids a periodic "sweep found 0 rows"
		// noise entry in the logs once the backfill is done.
		return nil
	}
	slog.Info("backfill sweeper: enqueuing", "count", len(rows))
	for _, r := range rows {
		if _, err := riverClient.Insert(ctx, jobs.BackfillRuntimeCredentialArgs{
			ProjectDatabaseID: r.ID,
		}, nil); err != nil {
			slog.Error("backfill sweeper: enqueue failed",
				"project_database_id", r.ID, "error", err)
			// keep going — one bad enqueue shouldn't stop the batch
		}
	}
	return nil
}
