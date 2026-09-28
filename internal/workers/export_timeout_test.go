package workers

import (
	"context"
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
func TestExportFailContext_SurvivesCanceledJob(t *testing.T) {
	jobCtx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, done := exportFailContext(jobCtx)
	defer done()
	if err := ctx.Err(); err != nil {
		t.Fatalf("fail context already done: %v", err)
	}
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > 15*time.Second {
		t.Errorf("fail context should be bounded, deadline %v ok=%v", dl, ok)
	}
}
