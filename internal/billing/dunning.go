package billing

import (
	"context"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/audit"
)

// RunDunningSweep advances subscriptions that have exited grace
// without recovering payment, and finalises cancel-at-period-end
// transitions. Intended to be invoked from a 1-hour cron tick in
// the worker pool (the existing River worker). Designed to be safe
// to call concurrently — every UPDATE is idempotent and bounded by
// a WHERE clause that only matches rows still in the transitional
// state.
//
// Returns the number of rows transitioned for observability /
// metrics. No error is returned for empty sweeps; a non-nil error
// only fires on DB transport failure.
func (s *Service) RunDunningSweep(ctx context.Context) (downgraded int, finalised int, err error) {
	// 1. Grace-period expired → downgrade.
	//    Move subscription to cancelled, flip project plan to free.
	gRows, err := s.pool.Query(ctx,
		`UPDATE public.subscriptions
		 SET status = $1
		 WHERE status = $2 AND grace_until IS NOT NULL AND grace_until < now()
		 RETURNING project_id`,
		StatusCancelled, StatusGrace,
	)
	if err != nil {
		return 0, 0, err
	}
	graceExpired := []string{}
	for gRows.Next() {
		var pid string
		if scanErr := gRows.Scan(&pid); scanErr == nil {
			graceExpired = append(graceExpired, pid)
		}
	}
	gRows.Close()

	for _, pid := range graceExpired {
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.projects SET plan = $2 WHERE id = $1 AND plan = $3`,
			pid, PlanFree, PlanPro,
		); err != nil {
			slog.Error("dunning: failed to downgrade project after grace expiry",
				"project", pid, "error", err)
			continue
		}
		downgraded++
		if s.audit != nil {
			s.audit.Log(ctx, pid, "", "", audit.ActionPlanChanged,
				audit.WithMetadata(map[string]any{
					"from":   PlanPro,
					"to":     PlanFree,
					"reason": "grace_period_expired",
				}))
		}
	}

	// 2. Cancel-at-period-end satisfied → downgrade.
	//    Subscription was already flagged by the user-initiated Cancel.
	cRows, err := s.pool.Query(ctx,
		`UPDATE public.subscriptions
		 SET status = $1
		 WHERE cancel_at_period_end = true
		   AND status = $2
		   AND current_period_end IS NOT NULL
		   AND current_period_end < now()
		 RETURNING project_id`,
		StatusCancelled, StatusProUntilPeriodEnd,
	)
	if err != nil {
		return downgraded, 0, err
	}
	periodEnded := []string{}
	for cRows.Next() {
		var pid string
		if scanErr := cRows.Scan(&pid); scanErr == nil {
			periodEnded = append(periodEnded, pid)
		}
	}
	cRows.Close()

	for _, pid := range periodEnded {
		if _, err := s.pool.Exec(ctx,
			`UPDATE public.projects SET plan = $2 WHERE id = $1 AND plan = $3`,
			pid, PlanFree, PlanPro,
		); err != nil {
			slog.Error("dunning: failed to downgrade project after period end",
				"project", pid, "error", err)
			continue
		}
		finalised++
		if s.audit != nil {
			s.audit.Log(ctx, pid, "", "", audit.ActionPlanChanged,
				audit.WithMetadata(map[string]any{
					"from":   PlanPro,
					"to":     PlanFree,
					"reason": "cancelled_at_period_end",
				}))
		}
	}

	// 3. Sweep stale pending_projects rows that never completed
	//    checkout (24-hour TTL from migration 000056).
	tag, perr := s.pool.Exec(ctx,
		`DELETE FROM public.pending_projects WHERE expires_at < now()`)
	if perr != nil {
		slog.Warn("dunning: pending_projects cleanup failed", "error", perr)
	} else if tag.RowsAffected() > 0 {
		slog.Info("dunning: cleaned up pending_projects",
			"count", tag.RowsAffected())
	}

	return downgraded, finalised, nil
}

// StartDunningWorker fires RunDunningSweep on a fixed interval. The
// existing River worker pool runs the gateway-side workers (DSAR
// export, etc.); this is a plain ticker because the sweep is
// idempotent + lightweight and doesn't need queue semantics.
//
// Stops when ctx is cancelled.
func (s *Service) StartDunningWorker(ctx context.Context) {
	const interval = 1 * time.Hour
	t := time.NewTicker(interval)
	defer t.Stop()
	slog.Info("dunning worker started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d, f, err := s.RunDunningSweep(ctx)
			if err != nil {
				slog.Error("dunning sweep failed", "error", err)
				continue
			}
			if d > 0 || f > 0 {
				slog.Info("dunning sweep completed",
					"downgraded_after_grace", d,
					"finalised_after_period_end", f)
			}
		}
	}
}
