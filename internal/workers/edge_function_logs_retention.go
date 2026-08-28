package workers

// Daily sweeper for public.edge_function_logs. Follow-up flagged in the
// #494 review of #492: before #492 each row was ~50 bytes (status,
// duration, method, error). With the new log_lines JSONB column (up
// to LOG_OUTPUT_LIMIT = 10 KB per invocation), a busy function
// logging a few INFO lines per call adds tens of MB per day and the
// table has no retention policy — it grows forever.
//
// This is the smallest possible fix: a daily ticker that runs a bulk
// DELETE for rows older than EDGE_FUNCTION_LOGS_RETENTION_DAYS.
// Pattern matches internal/workers/audit_retention.go (a plain
// time.Ticker rather than River, because retention is fixed-cadence
// housekeeping with no upstream trigger).
//
// Tier policy is intentionally NOT per-plan yet — we ship one number
// first, gather signal on volume, then split (e.g. Free 7d / Pro 30d)
// if a real customer complains that 30 days isn't enough or if the
// table still grows too fast at 30 days.

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	edgeFunctionLogsRetentionInterval = 24 * time.Hour
	edgeFunctionLogsRetentionDefault  = 30 // days
)

// StartEdgeFunctionLogsRetention launches the daily retention loop in
// a goroutine. Returns immediately; the loop exits when ctx is
// cancelled. Errors are logged and swallowed — the next tick reconciles.
//
// Config (env, read once at startup):
//   - EDGE_FUNCTION_LOGS_RETENTION_DAYS
//     Default 30. Set to 0 to disable (keeps rows forever — same
//     convention AUDIT_LOG_RETENTION_DAYS uses). Negative = disabled.
func StartEdgeFunctionLogsRetention(ctx context.Context, pool *pgxpool.Pool) {
	days := edgeFunctionLogsRetentionDefault
	if v := os.Getenv("EDGE_FUNCTION_LOGS_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			days = n
		}
	}

	if days == 0 {
		slog.Info("edge_function_logs retention disabled (EDGE_FUNCTION_LOGS_RETENTION_DAYS=0)")
		return
	}

	slog.Info("edge_function_logs retention worker started",
		"interval", edgeFunctionLogsRetentionInterval.String(),
		"retention_days", days,
	)

	go func() {
		// Run once at startup so a freshly-deployed pod doesn't wait
		// a full day before reclaiming space from any pre-existing
		// backlog.
		sweepEdgeFunctionLogs(ctx, pool, days)

		ticker := time.NewTicker(edgeFunctionLogsRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepEdgeFunctionLogs(ctx, pool, days)
			}
		}
	}()
}

// sweepEdgeFunctionLogs deletes rows past the retention horizon. One
// bulk statement rather than a chunked loop: existing indexes on
// (function_id, created_at DESC) and (project_id, created_at DESC)
// make the created_at scan cheap, and typical volumes for the first
// weeks post-#492 are small enough that a single DELETE won't hold a
// lock long enough to matter. If that changes (this shows up in slow
// query logs, or the delete rowcount jumps into the millions), swap
// to a chunked `DELETE … WHERE id = ANY(SELECT id … LIMIT 5000)`
// loop.
func sweepEdgeFunctionLogs(ctx context.Context, pool *pgxpool.Pool, days int) {
	start := time.Now()
	// $1 is interpolated as an interval via make_interval so we don't
	// concatenate an int into SQL (would work but sets a bad example).
	tag, err := pool.Exec(ctx,
		`DELETE FROM public.edge_function_logs
		 WHERE created_at < now() - make_interval(days => $1)`,
		days)
	if err != nil {
		slog.Error("edge_function_logs retention sweep failed",
			"error", err, "days", days)
		return
	}
	slog.Info("edge_function_logs retention sweep complete",
		"rows_deleted", tag.RowsAffected(),
		"retention_days", days,
		"duration_ms", time.Since(start).Milliseconds(),
	)
}
