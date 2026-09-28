package workers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
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

// A failed attempt is final on the last attempt, a timeout or a remote
// cancel; only a timeout is turned into a River cancel (no retry).
func TestExportFailure(t *testing.T) {
	live := context.Background()
	if final, nr := exportFailure(live, 1, 2); final || nr {
		t.Errorf("attempt 1 of 2: final=%v noRetry=%v, want false/false", final, nr)
	}
	if final, nr := exportFailure(live, 2, 2); !final || nr {
		t.Errorf("attempt 2 of 2: final=%v noRetry=%v, want true/false", final, nr)
	}
	timedOut, cancel := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer cancel()
	if final, nr := exportFailure(timedOut, 1, 2); !final || !nr {
		t.Errorf("timeout: final=%v noRetry=%v, want true/true", final, nr)
	}
	remote, rcancel := context.WithCancelCause(live)
	rcancel(rivertype.ErrJobCancelledRemotely)
	if final, nr := exportFailure(remote, 1, 2); !final || nr {
		t.Errorf("remote cancel: final=%v noRetry=%v, want true/false", final, nr)
	}
	shutdown, scancel := context.WithCancel(live)
	scancel()
	if final, _ := exportFailure(shutdown, 1, 2); final {
		t.Error("a plain cancel (shutdown) on attempt 1 is not final: River retries it")
	}

	boom := errors.New("boom")
	var ce *rivertype.JobCancelError
	if err := exportResult(boom, true); !errors.As(err, &ce) || !errors.Is(err, boom) {
		t.Errorf("noRetry: got %v, want a JobCancelError wrapping boom", err)
	}
	if err := exportResult(boom, false); err != boom {
		t.Errorf("retryable: got %v, want boom unchanged", err)
	}
	if err := exportResult(nil, true); err != nil {
		t.Errorf("success stays nil, got %v", err)
	}
}
