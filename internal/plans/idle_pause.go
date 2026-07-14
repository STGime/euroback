package plans

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// idlePauseThreshold is how long a Free project can go without a
// signed request before the idle-pause cron flips its state to
// 'paused'. 30 days, per the public-beta launch plan decision on
// idle-pause window (softer than Supabase's 7 days).
const idlePauseThreshold = 30 * 24 * time.Hour

// idlePausePollInterval is how often the cron scans `projects` for
// pause candidates. Hourly is enough: idle-pause is a coarse-grained
// signal, and the query is a cheap index scan against
// `idx_projects_idle_pause_candidates` (migration 000076).
const idlePausePollInterval = 1 * time.Hour

// IdlePauseWorker moves Free projects to `state = 'paused'` when
// `last_active_at` falls behind the threshold. Pro projects never
// pause. Grandfathered Free projects DO pause — the grandfather
// window is about numeric caps, not about the lifecycle model.
//
// The DB stays running throughout — Eurobase runs one shared Postgres
// cluster with per-tenant schemas, so "pause" only toggles the API +
// realtime + edge-function surface. See the wake-on-request
// middleware for the reverse direction. (public-beta launch plan
// decision #5.)
type IdlePauseWorker struct {
	pool *pgxpool.Pool
}

// NewIdlePauseWorker returns a worker ready to be Run from cmd/gateway.
func NewIdlePauseWorker(pool *pgxpool.Pool) *IdlePauseWorker {
	return &IdlePauseWorker{pool: pool}
}

// Run polls forever, pausing eligible projects on each tick. Returns
// when `ctx` is cancelled — the gateway process passes its
// long-lived context.
func (w *IdlePauseWorker) Run(ctx context.Context) {
	slog.Info("idle-pause worker started", "poll_interval", idlePausePollInterval, "threshold", idlePauseThreshold)
	// Fire once immediately so the pause backlog on process start
	// isn't held for a full poll interval.
	w.tick(ctx)
	t := time.NewTicker(idlePausePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("idle-pause worker stopped")
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// tick flips eligible projects. Idempotent: safe to run concurrently
// against the same DB (the WHERE clause is exact-match on `state` +
// `last_active_at`, so a row already paused by a peer is not
// re-updated). Logs but does not fail on transient errors — the next
// tick retries.
func (w *IdlePauseWorker) tick(ctx context.Context) {
	tag, err := w.pool.Exec(ctx,
		`UPDATE public.projects
		    SET state = 'paused'
		  WHERE plan = 'free'
		    AND state = 'active'
		    AND last_active_at IS NOT NULL
		    AND last_active_at < now() - $1::interval`,
		idlePauseThreshold.String(),
	)
	if err != nil {
		slog.Error("idle-pause tick failed", "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("idle-pause tick", "paused", n)
	}
}
