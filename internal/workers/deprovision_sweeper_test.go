package workers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
)

// TestRunDeprovisionSweeper_EnqueuesOnePerRow — pins the fan-out
// contract: N eligible rows → N enqueue calls, each carrying the
// row's ID. Locks the wiring against a future rename of Record.ID
// or a loop-variable capture bug (Go < 1.22 style).
func TestRunDeprovisionSweeper_EnqueuesOnePerRow(t *testing.T) {
	rows := []dbprovider.Record{
		{ID: "id-1"}, {ID: "id-2"}, {ID: "id-3"},
	}

	var listWindow time.Duration
	list := func(_ context.Context, window time.Duration) ([]dbprovider.Record, error) {
		listWindow = window
		return rows, nil
	}

	var enqueued []string
	enqueue := func(_ context.Context, id string) error {
		enqueued = append(enqueued, id)
		return nil
	}

	err := runDeprovisionSweeperWithDeps(context.Background(), list, enqueue, deprovisionRollbackWindow)
	if err != nil {
		t.Fatalf("sweep returned error: %v", err)
	}
	if listWindow != deprovisionRollbackWindow {
		t.Errorf("list called with window %v, want %v", listWindow, deprovisionRollbackWindow)
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

// TestRunDeprovisionSweeper_EmptyIsNoopAndNoEnqueue — the common
// case (no eligible rows) must not call the enqueuer and must not
// error. Guards against a future refactor that removes the empty-
// check and issues an unnecessary river insert.
func TestRunDeprovisionSweeper_EmptyIsNoopAndNoEnqueue(t *testing.T) {
	list := func(context.Context, time.Duration) ([]dbprovider.Record, error) {
		return nil, nil
	}

	var enqueueHits atomic.Int32
	enqueue := func(context.Context, string) error {
		enqueueHits.Add(1)
		return nil
	}

	err := runDeprovisionSweeperWithDeps(context.Background(), list, enqueue, deprovisionRollbackWindow)
	if err != nil {
		t.Fatalf("sweep returned error: %v", err)
	}
	if got := enqueueHits.Load(); got != 0 {
		t.Errorf("enqueue called %d times, want 0", got)
	}
}

// TestRunDeprovisionSweeper_ListErrorPropagates — the caller wants
// to see list errors so ops sees the "sweep failed" log line. The
// sweeper's outer loop swallows-and-logs; the inner func returns.
func TestRunDeprovisionSweeper_ListErrorPropagates(t *testing.T) {
	sentinel := errors.New("db unreachable")
	list := func(context.Context, time.Duration) ([]dbprovider.Record, error) {
		return nil, sentinel
	}
	enqueue := func(context.Context, string) error {
		t.Fatal("enqueue must not be called when list fails")
		return nil
	}

	err := runDeprovisionSweeperWithDeps(context.Background(), list, enqueue, deprovisionRollbackWindow)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want %v, got %v", sentinel, err)
	}
}

// TestRunDeprovisionSweeper_OneBadEnqueueDoesNotAbortBatch — one
// enqueue error must not stop the loop. Every eligible row should
// get its shot; the failing one's log line is the only signal.
// Without this, a single flaky River Insert would leave 99 other
// legitimately-eligible rows waiting a full hour for the next
// tick, extending the €60/mo bleed unnecessarily.
func TestRunDeprovisionSweeper_OneBadEnqueueDoesNotAbortBatch(t *testing.T) {
	rows := []dbprovider.Record{
		{ID: "id-a"}, {ID: "id-b"}, {ID: "id-c"},
	}
	list := func(context.Context, time.Duration) ([]dbprovider.Record, error) {
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

	err := runDeprovisionSweeperWithDeps(context.Background(), list, enqueue, deprovisionRollbackWindow)
	if err != nil {
		t.Fatalf("sweep returned error despite per-row enqueue failure: %v", err)
	}
	if len(attempted) != 3 {
		t.Errorf("attempted %d enqueues, want 3 (loop should continue past the middle failure)", len(attempted))
	}
}

// TestDeprovisionRollbackWindowIsSharedConstant — pins the value +
// documents that the worker's default and the sweeper's eligibility
// window MUST be the same constant. A future contributor changing
// one to (say) 14 days without changing the other would introduce
// silent noise (worker cancels enqueues) at best or a real gap at
// worst.
func TestDeprovisionRollbackWindowIsSharedConstant(t *testing.T) {
	want := 7 * 24 * time.Hour
	if deprovisionRollbackWindow != want {
		t.Errorf("deprovisionRollbackWindow = %v, want %v (matches plan doc + worker default)", deprovisionRollbackWindow, want)
	}
	// Worker cross-check: the DeprovisionTeamDatabaseWorker's zero-
	// RollbackWindow fallback falls back to deprovisionRollbackWindow
	// (see deprovision_team_db.go). A test that instantiates the
	// worker with RollbackWindow=0 and inspects the effective value
	// would need pool + registry — the constant equality above is
	// the pragmatic pin.
}
