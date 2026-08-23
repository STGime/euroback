package workers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/eurobase/euroback/internal/dbprovider"
)

// TestRunBackupScheduleSweeper_EnqueuesOnePerRow — locks the
// fan-out contract. Same shape as the #455 review response's
// deprovision-sweeper test.
func TestRunBackupScheduleSweeper_EnqueuesOnePerRow(t *testing.T) {
	rows := []dbprovider.BackupScheduleCandidate{
		{ID: "id-1"},
		{ID: "id-2"},
		{ID: "id-3"},
	}

	var listLimit int
	list := func(_ context.Context, limit int) ([]dbprovider.BackupScheduleCandidate, error) {
		listLimit = limit
		return rows, nil
	}

	var enqueued []string
	enqueue := func(_ context.Context, id string) error {
		enqueued = append(enqueued, id)
		return nil
	}

	err := runBackupScheduleSweeperWithDeps(context.Background(), list, enqueue, backupScheduleBatchSize)
	if err != nil {
		t.Fatalf("sweep returned error: %v", err)
	}
	if listLimit != backupScheduleBatchSize {
		t.Errorf("list called with limit %d, want %d", listLimit, backupScheduleBatchSize)
	}
	if len(enqueued) != len(rows) {
		t.Fatalf("enqueued %d ids, want %d", len(enqueued), len(rows))
	}
	for i, r := range rows {
		if enqueued[i] != r.ID {
			t.Errorf("enqueued[%d] = %q, want %q", i, enqueued[i], r.ID)
		}
	}
}

// TestRunBackupScheduleSweeper_EmptyIsNoop — steady state (all
// active rows already stamped) must not error and must not enqueue.
func TestRunBackupScheduleSweeper_EmptyIsNoop(t *testing.T) {
	list := func(context.Context, int) ([]dbprovider.BackupScheduleCandidate, error) {
		return nil, nil
	}
	var hits atomic.Int32
	enqueue := func(context.Context, string) error {
		hits.Add(1)
		return nil
	}
	if err := runBackupScheduleSweeperWithDeps(context.Background(), list, enqueue, backupScheduleBatchSize); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("enqueue called %d times, want 0", got)
	}
}

// TestRunBackupScheduleSweeper_ListErrorPropagates — caller wants
// to see list errors (surfaces as the "sweep failed" log line via
// StartBackupScheduleSweeper's outer wrapper).
func TestRunBackupScheduleSweeper_ListErrorPropagates(t *testing.T) {
	sentinel := errors.New("db unreachable")
	list := func(context.Context, int) ([]dbprovider.BackupScheduleCandidate, error) {
		return nil, sentinel
	}
	enqueue := func(context.Context, string) error {
		t.Fatal("enqueue must not be called when list fails")
		return nil
	}
	if err := runBackupScheduleSweeperWithDeps(context.Background(), list, enqueue, backupScheduleBatchSize); !errors.Is(err, sentinel) {
		t.Fatalf("want %v, got %v", sentinel, err)
	}
}

// TestRunBackupScheduleSweeper_OneBadEnqueueDoesNotAbortBatch —
// mirrors the #455 review-response test. One flaky Insert must
// not delay 99 legitimate rows by a full hour.
func TestRunBackupScheduleSweeper_OneBadEnqueueDoesNotAbortBatch(t *testing.T) {
	rows := []dbprovider.BackupScheduleCandidate{
		{ID: "id-a"},
		{ID: "id-b"},
		{ID: "id-c"},
	}
	list := func(context.Context, int) ([]dbprovider.BackupScheduleCandidate, error) {
		return rows, nil
	}
	var attempted []string
	enqueue := func(_ context.Context, id string) error {
		attempted = append(attempted, id)
		if id == "id-b" {
			return errors.New("river insert failed")
		}
		return nil
	}
	if err := runBackupScheduleSweeperWithDeps(context.Background(), list, enqueue, backupScheduleBatchSize); err != nil {
		t.Fatalf("sweep returned error despite per-row enqueue failure: %v", err)
	}
	if len(attempted) != 3 {
		t.Errorf("attempted %d enqueues, want 3", len(attempted))
	}
}
