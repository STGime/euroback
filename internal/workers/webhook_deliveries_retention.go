package workers

// Daily sweep of public.webhook_deliveries (#845). Deliveries are a
// debugging aid in the console, not a record to keep: default 30 days,
// WEBHOOK_DELIVERIES_RETENTION_DAYS overrides (0 disables).

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	webhookDeliveriesRetentionInterval = 24 * time.Hour
	webhookDeliveriesRetentionDefault  = 30 // days
)

// StartWebhookDeliveriesRetention starts the daily sweep; returns at once.
func StartWebhookDeliveriesRetention(ctx context.Context, pool *pgxpool.Pool) {
	days := webhookDeliveriesRetentionDefault
	if v := os.Getenv("WEBHOOK_DELIVERIES_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	if days <= 0 {
		slog.Info("webhook_deliveries retention disabled", "WEBHOOK_DELIVERIES_RETENTION_DAYS", days)
		return
	}
	go func() {
		sweepWebhookDeliveries(ctx, pool, days)
		ticker := time.NewTicker(webhookDeliveriesRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepWebhookDeliveries(ctx, pool, days)
			}
		}
	}()
}

// sweepWebhookDeliveries uses the existing created_at index.
func sweepWebhookDeliveries(ctx context.Context, pool *pgxpool.Pool, days int) {
	tag, err := pool.Exec(ctx,
		`DELETE FROM public.webhook_deliveries WHERE created_at < now() - make_interval(days => $1)`, days)
	if err != nil {
		slog.Error("webhook_deliveries retention sweep failed", "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("webhook_deliveries retention sweep", "deleted", n, "retention_days", days)
	}
}
