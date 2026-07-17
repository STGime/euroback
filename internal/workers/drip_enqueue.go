package workers

import (
	"context"
	"fmt"
	"time"

	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// EnqueueOnboardingSeries inserts NumOnboardingSteps River jobs for
// the drip series, one per step, spaced by OnboardingIntervalDays.
// Called from the platform signup handler in the same transaction
// as the platform_users insert — so a signup that succeeds always
// gets its drip queued, and one that rolls back leaves no orphan
// jobs.
//
// Phase C of the public-beta launch plan.
//
// Uses `InsertTx` (transactional) rather than `Insert` so a rollback
// of the outer tx also rolls back the enqueue.
func EnqueueOnboardingSeries(ctx context.Context, riverClient *river.Client[pgx.Tx], tx pgx.Tx, userID string, signupTime time.Time) error {
	for step := 0; step < email.NumOnboardingSteps; step++ {
		scheduledAt := signupTime.Add(time.Duration(step) * email.OnboardingIntervalDays * 24 * time.Hour)
		_, err := riverClient.InsertTx(ctx, tx,
			jobs.SendDripEmailArgs{UserID: userID, Step: step},
			&river.InsertOpts{ScheduledAt: scheduledAt},
		)
		if err != nil {
			return fmt.Errorf("enqueue drip step %d: %w", step, err)
		}
	}
	return nil
}
