package workers

// #354 scheduler — every 30s, walk enabled webhook destinations and
// enqueue one River deliver job per destination. UniqueOpts.ByArgs
// on DeliverAuditWebhookArgs collapses duplicates so a slow sink
// can't queue up multiple in-flight jobs against the same row.
//
// Not itself a River job (like StartAuditRetention /
// StartRetentionHoldSweeper): fixed-cadence housekeeping with no
// upstream trigger. River is used only for the DELIVERY jobs it
// enqueues, so River's exponential-backoff machinery handles the
// per-attempt retry math without us re-implementing it.

import (
	"context"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const auditWebhookSchedulerInterval = 30 * time.Second

// StartAuditWebhookScheduler launches the sweeper loop. Returns
// immediately; the loop exits when ctx is cancelled.
func StartAuditWebhookScheduler(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) {
	if riverClient == nil {
		slog.Warn("audit webhook scheduler: river client not configured, scheduler disabled")
		return
	}
	go func() {
		// Initial tick so a freshly-rolled worker doesn't wait 30s
		// before its first enqueue pass.
		if err := runAuditWebhookScheduler(ctx, pool, riverClient); err != nil {
			slog.Error("audit webhook scheduler: initial run failed", "error", err)
		}
		t := time.NewTicker(auditWebhookSchedulerInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runAuditWebhookScheduler(ctx, pool, riverClient); err != nil {
					slog.Error("audit webhook scheduler: run failed", "error", err)
				}
			}
		}
	}()
	slog.Info("audit webhook scheduler started", "interval", auditWebhookSchedulerInterval)
}

func runAuditWebhookScheduler(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) error {
	// Enabled webhook destinations only; syslog gets its own worker
	// (#355). Cap at 500 per pass — a tenant with more enabled sinks
	// than that is out of the intended usage pattern; log a warn.
	rows, err := pool.Query(ctx,
		`SELECT id::text
		   FROM public.audit_export_destinations
		  WHERE enabled = true AND kind = $1
		  ORDER BY last_cursor ASC
		  LIMIT 500`,
		string(compliance.DestinationWebhook),
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		enqueued int
		skipped  int
	)
	for rows.Next() {
		var destID string
		if err := rows.Scan(&destID); err != nil {
			continue
		}
		_, err := riverClient.Insert(ctx, jobs.DeliverAuditWebhookArgs{DestinationID: destID}, nil)
		if err != nil {
			// UniqueOpts collision → a job is already pending/running
			// for this destination. That's the intended behaviour; log
			// as a skip, not an error.
			skipped++
			continue
		}
		enqueued++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if enqueued > 0 || skipped > 0 {
		slog.Debug("audit webhook scheduler tick",
			"enqueued", enqueued, "skipped_dedup", skipped)
	}
	return nil
}

