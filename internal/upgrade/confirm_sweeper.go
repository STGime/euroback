package upgrade

import (
	"context"
	"log/slog"
	"time"
)

// confirmSweepInterval is how often ConfirmSweeper.RunSweep runs.
// Hourly ticks give 24 chances per day to move live upgrades past
// the 7-day mark — the "day-scale" precision the confirmation
// window actually needs.
const confirmSweepInterval = 1 * time.Hour

// confirmationAgeThreshold is how long a `live` upgrade waits before
// the sweeper transitions it to `confirmed` and purges the source
// data from the shared cluster. Matches the project_databases
// deleted_at 7-day window (000083) — consistency across the two
// rollback-window features.
const confirmationAgeThreshold = 7 * 24 * time.Hour

// ConfirmSweeper transitions live upgrades past their 7-day
// rollback window to `confirmed`. Runs every hour. Data-purge from
// the shared cluster (DROP SCHEMA + DELETE per-project public rows)
// is deferred to a follow-up PR alongside the copy work — this
// sweeper marks the row `confirmed` even though nothing is deleted
// yet, matching the current upgrade worker's "copy stubbed" posture.
//
// Reads through the Service to reuse ConfirmUpgrade's transaction +
// migrator-role guarantee.
type ConfirmSweeper struct {
	svc *Service
}

func NewConfirmSweeper(svc *Service) *ConfirmSweeper {
	return &ConfirmSweeper{svc: svc}
}

// StartLoop launches the hourly sweep in a goroutine. Returns
// immediately; the loop exits when ctx is cancelled.
//
// Errors from a single tick are logged but never propagated — the
// scheduler must never die because one project's DB flapped.
func (s *ConfirmSweeper) StartLoop(ctx context.Context) {
	go func() {
		// Run once on start so an operator restarting the gateway
		// doesn't have to wait 60 minutes for the first pass.
		s.RunSweep(ctx)
		t := time.NewTicker(confirmSweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.RunSweep(ctx)
			}
		}
	}()
}

// RunSweep is exported so tests can drive one tick at a time.
func (s *ConfirmSweeper) RunSweep(ctx context.Context) {
	rows, err := s.svc.pool.Query(ctx,
		`SELECT id::text
		   FROM public.project_upgrades
		  WHERE state = 'live'
		    AND cutover_at IS NOT NULL
		    AND cutover_at < now() - $1::interval
		  ORDER BY cutover_at ASC
		  LIMIT 100`,
		confirmationAgeThreshold.String(),
	)
	if err != nil {
		slog.Error("upgrade confirm sweep query failed", "error", err)
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			slog.Error("upgrade confirm sweep scan failed", "error", err)
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		slog.Error("upgrade confirm sweep rows.Err", "error", err)
		return
	}

	for _, id := range ids {
		if err := s.svc.ConfirmUpgrade(ctx, id); err != nil {
			slog.Error("upgrade confirm sweep confirm failed",
				"upgrade_id", id, "error", err)
			continue
		}
		slog.Info("upgrade confirm sweep transitioned live → confirmed",
			"upgrade_id", id)
	}
}
