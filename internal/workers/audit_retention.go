package workers

// Periodic retention task for the two audit streams. Closes #171.
//
// Not a River job: River is great for one-shot work triggered by an HTTP
// handler or another job. Retention is fixed-cadence housekeeping with no
// upstream trigger, and the worker process already runs a 60-second cron
// ticker for the same reason (cmd/worker/main.go). We add a separate,
// slower ticker for retention rather than coupling it to cron — cron fires
// every minute; retention runs daily.

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/jackc/pgx/v5/pgxpool"
)

// auditRetentionInterval is how often Run is invoked. Daily is plenty —
// neither partition drops nor row pruning are time-sensitive at finer
// resolution, and a slower cadence lets ad-hoc operator runs (the SQL
// helpers are idempotent) drive the system when needed.
const auditRetentionInterval = 24 * time.Hour

// StartAuditRetention launches the daily retention loop in a goroutine.
// It returns immediately; the loop exits when ctx is cancelled. Errors
// from a single tick are logged but never propagated — retention is
// best-effort, idempotent, and the next tick reconciles.
//
// Config is read from environment variables once at startup:
//   - AUDIT_LOG_RETENTION_DAYS              (default 0 = never)
//   - DATA_ACCESS_LOG_RETENTION_MONTHS      (default 13)
//   - DATA_ACCESS_LOG_FUTURE_MONTHS_AHEAD   (default 12)
func StartAuditRetention(ctx context.Context, pool *pgxpool.Pool) {
	cfg := audit.DefaultRetentionConfig()
	if v := os.Getenv("AUDIT_LOG_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.AuditLogRetentionDays = n
		}
	}
	if v := os.Getenv("DATA_ACCESS_LOG_RETENTION_MONTHS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.DataAccessLogRetentionMonths = n
		}
	}
	if v := os.Getenv("DATA_ACCESS_LOG_FUTURE_MONTHS_AHEAD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.FuturePartitionsMonthsAhead = n
		}
	}

	svc := audit.NewRetentionService(pool)

	slog.Info("audit retention worker started",
		"interval", auditRetentionInterval.String(),
		"audit_log_retention_days", cfg.AuditLogRetentionDays,
		"data_access_log_retention_months", cfg.DataAccessLogRetentionMonths,
		"future_partitions_months_ahead", cfg.FuturePartitionsMonthsAhead,
	)

	go func() {
		// Run once at startup so a freshly-deployed pod doesn't wait a
		// full day before reconciling forward partitions.
		runOnce(ctx, svc, cfg)

		ticker := time.NewTicker(auditRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOnce(ctx, svc, cfg)
			}
		}
	}()
}

func runOnce(ctx context.Context, svc *audit.RetentionService, cfg audit.RetentionConfig) {
	res, err := svc.Run(ctx, cfg)
	if err != nil {
		slog.Error("audit retention run failed", "error", err)
		return
	}
	slog.Info("audit retention run complete",
		"audit_log_rows_deleted", res.AuditLogRowsDeleted,
		"data_access_partitions_ensured", res.DataAccessPartitionsEnsured,
		"data_access_partitions_dropped", res.DataAccessPartitionsDropped,
	)
}
