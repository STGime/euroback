package workers

// Legal-Team M2b (#314) — daily sweeper that purges expired
// retention holds.
//
// A retention hold pins a row / object / table under a legal
// basis (§50 BRAO, §257 HGB, §147 AO, or free-text) until its
// `expires_at`. Once expired, the target becomes erasable again —
// the erasure-path IsHeld check no longer finds a matching row.
//
// This sweeper is the mechanical purge of the hold *record itself*.
// Keeping expired rows around would grow the table indefinitely
// and complicate audits ("is this hold still meaningful or just
// residue?"). Daily cadence is plenty — retention windows are
// measured in years, not hours.
//
// Not a River job — same reasoning as StartBackupSweeper and
// StartAuditRetention: fixed-cadence housekeeping with no upstream
// trigger.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const retentionHoldSweeperInterval = 24 * time.Hour

// StartRetentionHoldSweeper launches the daily sweeper. Returns
// immediately; loop exits when ctx is cancelled.
//
// Fires an initial run at pod start rather than waiting a full
// 24h — a freshly-rolled worker shouldn't leave a batch of
// expired-yesterday holds sitting until tomorrow.
func StartRetentionHoldSweeper(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		if err := runRetentionHoldSweeper(ctx, pool); err != nil {
			slog.Error("retention hold sweeper: initial run failed", "error", err)
		}
		t := time.NewTicker(retentionHoldSweeperInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runRetentionHoldSweeper(ctx, pool); err != nil {
					slog.Error("retention hold sweeper: run failed", "error", err)
				}
			}
		}
	}()
	slog.Info("retention hold sweeper started", "interval", retentionHoldSweeperInterval)
}

func runRetentionHoldSweeper(ctx context.Context, pool *pgxpool.Pool) error {
	tag, err := pool.Exec(ctx,
		`DELETE FROM public.retention_holds WHERE expires_at < now()`,
	)
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("retention hold sweeper: purged expired holds", "count", n)
	}
	return nil
}
