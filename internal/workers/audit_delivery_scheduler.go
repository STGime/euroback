package workers

// Scheduler for #354 (webhook) + #355 (syslog) SIEM deliverers.
// Every 30s, walk enabled destinations and enqueue one River job
// per row — kind-aware: webhook rows get DeliverAuditWebhookArgs,
// syslog rows get DeliverAuditSyslogArgs. UniqueOpts.ByArgs on
// both job types collapses duplicates so a slow sink of either
// kind can't queue up multiple in-flight jobs against the same row.
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

// auditDeliverySchedulerInterval is how often the scheduler
// enqueues delivery jobs. 30s is aggressive enough to feel
// "real-time" for a SIEM without spamming River — a slow sink
// stays covered by UniqueOpts + River's backoff.
const auditDeliverySchedulerInterval = 30 * time.Second

// StartAuditDeliveryScheduler launches the sweeper loop. Returns
// immediately; the loop exits when ctx is cancelled. Wired from
// cmd/worker/main.go alongside the other fixed-cadence sweepers.
//
// Renamed from StartAuditWebhookScheduler now that #355 adds
// syslog — same shape, kind-aware enqueue.
func StartAuditDeliveryScheduler(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) {
	if riverClient == nil {
		slog.Warn("audit delivery scheduler: river client not configured, scheduler disabled")
		return
	}
	go func() {
		if err := runAuditDeliveryScheduler(ctx, pool, riverClient); err != nil {
			slog.Error("audit delivery scheduler: initial run failed", "error", err)
		}
		t := time.NewTicker(auditDeliverySchedulerInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runAuditDeliveryScheduler(ctx, pool, riverClient); err != nil {
					slog.Error("audit delivery scheduler: run failed", "error", err)
				}
			}
		}
	}()
	slog.Info("audit delivery scheduler started", "interval", auditDeliverySchedulerInterval)
}

func runAuditDeliveryScheduler(ctx context.Context, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) error {
	rows, err := pool.Query(ctx,
		`SELECT id::text, kind
		   FROM public.audit_export_destinations
		  WHERE enabled = true
		  ORDER BY last_cursor ASC
		  LIMIT 500`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var enqueued, skipped int
	for rows.Next() {
		var (
			destID string
			kind   string
		)
		if err := rows.Scan(&destID, &kind); err != nil {
			continue
		}
		var insertErr error
		switch kind {
		case string(compliance.DestinationWebhook):
			_, insertErr = riverClient.Insert(ctx, jobs.DeliverAuditWebhookArgs{DestinationID: destID}, nil)
		case string(compliance.DestinationSyslog):
			_, insertErr = riverClient.Insert(ctx, jobs.DeliverAuditSyslogArgs{DestinationID: destID}, nil)
		default:
			// Unknown kind → skip. The DB CHECK constraint should
			// prevent this row from existing at all; treat as a
			// defensive no-op.
			continue
		}
		if insertErr != nil {
			// UniqueOpts collision → a job is already pending or
			// running for this destination. Intended behaviour;
			// log as a skip.
			skipped++
			continue
		}
		enqueued++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if enqueued > 0 || skipped > 0 {
		slog.Debug("audit delivery scheduler tick",
			"enqueued", enqueued, "skipped_dedup", skipped)
	}
	return nil
}
