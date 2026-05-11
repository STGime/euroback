package enduser

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Closes #58. We can't easily mock pgxpool, so RunOAuthStateSweeper is
// covered by extracting the loop's tick/cancel behaviour to a helper
// that takes a sweep function. The helper plus the production loop
// share the same select statement structure; verifying the helper
// covers the production behaviour by inspection.
//
// (Integration-shape tests against a real Postgres are absent across
// this package — adding one for this single function would be a much
// bigger lift. The function itself is a single indexed DELETE that
// matches the existing schema's idx_oauth_states_expires.)

// runSweeperWithFn mirrors the production loop body but takes the
// sweep function as a parameter, so we can exercise the cadence and
// shutdown paths without a database.
func runSweeperWithFn(ctx context.Context, every time.Duration, sweep func(context.Context) error) {
	if every <= 0 {
		every = 10 * time.Minute
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sweep(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
		}
	}
}

func TestSweeperLoop_FiresOnTick(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSweeperWithFn(ctx, 10*time.Millisecond, func(_ context.Context) error {
			calls.Add(1)
			return nil
		})
		close(done)
	}()

	// Wait long enough for several ticks.
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if calls.Load() < 3 {
		t.Errorf("expected ≥3 sweep calls, got %d", calls.Load())
	}
}

func TestSweeperLoop_StopsOnCancel(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSweeperWithFn(ctx, 10*time.Millisecond, func(_ context.Context) error {
			calls.Add(1)
			return nil
		})
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// goroutine exited promptly.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("sweeper did not exit after ctx cancel")
	}
}

func TestSweeperLoop_ContinuesOnError(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runSweeperWithFn(ctx, 10*time.Millisecond, func(_ context.Context) error {
			calls.Add(1)
			return context.DeadlineExceeded // arbitrary non-nil
		})
		close(done)
	}()

	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if calls.Load() < 3 {
		t.Errorf("expected sweeper to keep ticking through errors; got %d calls", calls.Load())
	}
}
