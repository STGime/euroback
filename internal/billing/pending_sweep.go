package billing

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pendingSweepInterval is how often PendingSweeper runs. Hourly
// matches the downgrade sweeper's cadence; the pending_projects
// rows have expires_at defaulting to now + 24h, so hourly gives us
// ~1h resolution on when abandoned intents are cleaned up.
const pendingSweepInterval = 1 * time.Hour

// PendingSweeper deletes pending_projects rows that never turned
// into a real project — either the user closed the tab before
// completing Mollie's checkout (row has mollie_payment_id but
// Mollie's webhook never confirmed), or the Mollie call itself
// errored (row has NULL mollie_payment_id).
//
// Only rows past expires_at AND without a resolved payment are
// swept. A row WITH mollie_payment_id past expires_at means Mollie
// took a payment that our webhook never resolved — those are left
// alone and surfaced via slog for ops attention (should not
// happen in normal operation, indicates a webhook delivery issue).
//
// See issue #406 for the payment-first project creation flow this
// supports; migration 000102 for the pending_projects table.
type PendingSweeper struct {
	pool *pgxpool.Pool
}

// NewPendingSweeper constructs the sweeper. Wired from
// cmd/gateway/main.go alongside the downgrade sweep.
func NewPendingSweeper(pool *pgxpool.Pool) *PendingSweeper {
	return &PendingSweeper{pool: pool}
}

// StartLoop launches the sweep in a goroutine. Same shape as
// DowngradeService.StartLoop — fire once on startup, then hourly
// on the ticker; exits when ctx is cancelled.
func (s *PendingSweeper) StartLoop(ctx context.Context) {
	go func() {
		s.RunSweep(ctx)
		ticker := time.NewTicker(pendingSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunSweep(ctx)
			}
		}
	}()
	slog.Info("billing: pending-project sweep loop started", "interval", pendingSweepInterval)
}

// RunSweep is the tick body. Exposed for manual invocation from an
// ops runbook. Two branches:
//
//  1. Unresolved (no Mollie payment ID) past expires_at → DELETE.
//     User clicked Create but Mollie was never called, or errored.
//  2. Resolved (has Mollie payment ID) past expires_at → LOG.
//     Mollie took a payment we haven't processed. Should not
//     happen; surfaces webhook delivery issues.
func (s *PendingSweeper) RunSweep(ctx context.Context) {
	// Branch 1: hard-delete unresolved intents.
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM public.pending_projects
		  WHERE mollie_payment_id IS NULL
		    AND expires_at < now()`,
	)
	if err != nil {
		slog.Error("billing.pending_sweep.delete_unresolved_failed", "error", err)
	} else if n := tag.RowsAffected(); n > 0 {
		slog.Info("billing.pending_sweep.deleted_unresolved", "count", n)
	}

	// Branch 2: surface resolved-but-stale intents. We don't
	// auto-refund here — the payment is real and the webhook
	// might yet resolve. Just tell ops.
	rows, err := s.pool.Query(ctx,
		`SELECT id, owner_id::text, mollie_payment_id, expires_at
		   FROM public.pending_projects
		  WHERE mollie_payment_id IS NOT NULL
		    AND expires_at < now()`,
	)
	if err != nil {
		slog.Error("billing.pending_sweep.list_stale_resolved_failed", "error", err)
		return
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var id, ownerID, molliePaymentID string
		var expiresAt time.Time
		if err := rows.Scan(&id, &ownerID, &molliePaymentID, &expiresAt); err != nil {
			slog.Error("billing.pending_sweep.scan_failed", "error", err)
			continue
		}
		slog.Warn("billing.pending_sweep.stale_resolved_intent",
			"pending_project_id", id,
			"owner_id", ownerID,
			"mollie_payment_id", molliePaymentID,
			"expires_at", expiresAt,
			"note", "Mollie took a payment for a project we never created — check webhook delivery",
		)
		count++
	}
	if count > 0 {
		slog.Warn("billing.pending_sweep.stale_resolved_summary", "count", count)
	}
}
