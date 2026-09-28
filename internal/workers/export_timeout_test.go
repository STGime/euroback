package workers

import (
	"context"
	"errors"
	"testing"
	"time"
)

// #370: River's default job timeout is 1 minute; a large export must get
// exportJobTimeout. Both export kinds.
func TestExportWorkers_Timeout(t *testing.T) {
	if got := (&TenantExportWorker{}).Timeout(nil); got != exportJobTimeout {
		t.Errorf("TenantExportWorker.Timeout = %v, want %v", got, exportJobTimeout)
	}
	if got := (&UserExportWorker{}).Timeout(nil); got != exportJobTimeout {
		t.Errorf("UserExportWorker.Timeout = %v, want %v", got, exportJobTimeout)
	}
	if exportJobTimeout < 30*time.Minute {
		t.Errorf("exportJobTimeout = %v, want >= 30m", exportJobTimeout)
	}
	if exportSlowAfter >= exportJobTimeout {
		t.Error("the slow-export warning must fire before the timeout")
	}
}

// Failure bookkeeping must still work after the job timeout canceled the
// job context — otherwise the export stays "running" forever.
func TestExportBookkeepingContext_SurvivesCanceledJob(t *testing.T) {
	jobCtx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, done := exportBookkeepingContext(jobCtx)
	defer done()
	if err := ctx.Err(); err != nil {
		t.Fatalf("fail context already done: %v", err)
	}
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > 15*time.Second {
		t.Errorf("fail context should be bounded, deadline %v ok=%v", dl, ok)
	}
}

// A failed attempt is final on the last attempt or on a timeout (which is
// not retried); earlier failures leave the export "running" for the retry.
func TestExportAttemptFinal(t *testing.T) {
	live := context.Background()
	if exportAttemptFinal(live, 1, 2) {
		t.Error("attempt 1 of 2 is not final")
	}
	if !exportAttemptFinal(live, 2, 2) {
		t.Error("attempt 2 of 2 is final")
	}
	timedOut, cancel := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer cancel()
	if !exportAttemptFinal(timedOut, 1, 2) {
		t.Error("a timed-out attempt is final")
	}
	// A timeout becomes a River cancel (no retry); other errors pass through.
	boom := errors.New("boom")
	if err := exportResult(timedOut, boom); err == boom || !errors.Is(err, boom) {
		t.Errorf("timeout: got %v, want a JobCancel wrapping boom", err)
	}
	if err := exportResult(live, boom); err != boom {
		t.Errorf("no timeout: got %v, want boom unchanged", err)
	}
	if err := exportResult(timedOut, nil); err != nil {
		t.Errorf("success stays nil, got %v", err)
	}
}
