package workers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// stubJob wraps a *river.Job[T] with a non-nil embedded *JobRow so
// Worker code that reads job.ID (idempotency key) doesn't dereference
// nil. In production, River always populates JobRow from Postgres —
// this is a test-construction quirk only.
func stubJob[T river.JobArgs](args T, id int64) *river.Job[T] {
	return &river.Job[T]{
		JobRow: &rivertype.JobRow{ID: id},
		Args:   args,
	}
}

// mockProvider is an in-memory Provider used to exercise the worker's
// polling loop, retry classification, and error branches without a
// real network or DB.
//
// Behaviour is controlled by per-method function fields — tests set
// only the ones they need.
type mockProvider struct {
	name         string
	provisionFn  func(ctx context.Context, opts dbprovider.ProvisionOpts) (*dbprovider.Instance, error)
	describeFn   func(ctx context.Context, id string) (*dbprovider.Instance, error)
	deleteFn     func(ctx context.Context, id string) error
	snapshotFn   func(ctx context.Context, id string) (*dbprovider.Snapshot, error)
	listSnapsFn  func(ctx context.Context, id string) ([]dbprovider.Snapshot, error)
	restoreFn    func(ctx context.Context, id string, src dbprovider.RestoreSource) (*dbprovider.Instance, error)
	healthFn     func(ctx context.Context, id string) (dbprovider.State, error)
	describeHits atomic.Int32
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Provision(ctx context.Context, opts dbprovider.ProvisionOpts) (*dbprovider.Instance, error) {
	return m.provisionFn(ctx, opts)
}
func (m *mockProvider) Snapshot(ctx context.Context, id string) (*dbprovider.Snapshot, error) {
	return m.snapshotFn(ctx, id)
}
func (m *mockProvider) ListSnapshots(ctx context.Context, id string) ([]dbprovider.Snapshot, error) {
	return m.listSnapsFn(ctx, id)
}
func (m *mockProvider) Restore(ctx context.Context, id string, src dbprovider.RestoreSource) (*dbprovider.Instance, error) {
	return m.restoreFn(ctx, id, src)
}
func (m *mockProvider) Delete(ctx context.Context, id string) error {
	return m.deleteFn(ctx, id)
}
func (m *mockProvider) Health(ctx context.Context, id string) (dbprovider.State, error) {
	return m.healthFn(ctx, id)
}
func (m *mockProvider) Describe(ctx context.Context, id string) (*dbprovider.Instance, error) {
	m.describeHits.Add(1)
	return m.describeFn(ctx, id)
}

// TestPollUntilActive_HappyPath — state=active + endpoint populated
// on the second Describe call.
func TestPollUntilActive_HappyPath(t *testing.T) {
	var calls int32
	m := &mockProvider{
		describeFn: func(ctx context.Context, id string) (*dbprovider.Instance, error) {
			n := atomic.AddInt32(&calls, 1)
			if n < 2 {
				return &dbprovider.Instance{State: dbprovider.StateProvisioning}, nil
			}
			return &dbprovider.Instance{
				State: dbprovider.StateActive,
				Host:  "51.15.1.2",
				Port:  5432,
			}, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	inst, err := pollUntilActive(ctx, m, "rdb-1", 5*time.Millisecond, testLogger())
	if err != nil {
		t.Fatalf("pollUntilActive: %v", err)
	}
	if inst.Host != "51.15.1.2" || inst.Port != 5432 {
		t.Errorf("endpoint: %+v", inst)
	}
	if calls < 2 {
		t.Errorf("expected at least 2 Describe calls, got %d", calls)
	}
}

// TestPollUntilActive_FailedIsTerminal — provider says failed, loop
// returns error immediately (no more polling).
func TestPollUntilActive_FailedIsTerminal(t *testing.T) {
	m := &mockProvider{
		describeFn: func(ctx context.Context, id string) (*dbprovider.Instance, error) {
			return &dbprovider.Instance{State: dbprovider.StateFailed}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := pollUntilActive(ctx, m, "rdb-1", 5*time.Millisecond, testLogger())
	if err == nil || err.Error() != "provider reports failed state" {
		t.Errorf("want failed-state error, got %v", err)
	}
}

// TestPollUntilActive_TimeoutOnStuck — provider stays provisioning;
// loop returns context.DeadlineExceeded when the timeout fires.
func TestPollUntilActive_TimeoutOnStuck(t *testing.T) {
	m := &mockProvider{
		describeFn: func(ctx context.Context, id string) (*dbprovider.Instance, error) {
			return &dbprovider.Instance{State: dbprovider.StateProvisioning}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := pollUntilActive(ctx, m, "rdb-1", 5*time.Millisecond, testLogger())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want DeadlineExceeded, got %v", err)
	}
}

// TestPollUntilActive_TolerateTransientDescribeErrors — describe
// returning an error is retryable; loop continues.
func TestPollUntilActive_TolerateTransientDescribeErrors(t *testing.T) {
	var calls int32
	m := &mockProvider{
		describeFn: func(ctx context.Context, id string) (*dbprovider.Instance, error) {
			n := atomic.AddInt32(&calls, 1)
			if n == 1 {
				return nil, errors.New("transient blip")
			}
			return &dbprovider.Instance{
				State: dbprovider.StateActive,
				Host:  "h", Port: 5432,
			}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	inst, err := pollUntilActive(ctx, m, "rdb-1", 5*time.Millisecond, testLogger())
	if err != nil {
		t.Fatalf("want success after retry, got %v", err)
	}
	if inst.Host != "h" {
		t.Errorf("host: got %q", inst.Host)
	}
}

// TestPollUntilActive_ActiveButEmptyEndpointKeepsPolling — some
// providers flip to state=active before the endpoint list is
// populated; the loop keeps polling until both are ready.
func TestPollUntilActive_ActiveButEmptyEndpointKeepsPolling(t *testing.T) {
	var calls int32
	m := &mockProvider{
		describeFn: func(ctx context.Context, id string) (*dbprovider.Instance, error) {
			n := atomic.AddInt32(&calls, 1)
			if n < 3 {
				// active but no endpoint yet
				return &dbprovider.Instance{State: dbprovider.StateActive}, nil
			}
			return &dbprovider.Instance{
				State: dbprovider.StateActive,
				Host:  "h", Port: 5432,
			}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	inst, err := pollUntilActive(ctx, m, "rdb-1", 5*time.Millisecond, testLogger())
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if inst.Port == 0 {
		t.Error("port not populated on returned instance")
	}
	if calls < 3 {
		t.Errorf("expected at least 3 Describe calls, got %d", calls)
	}
}

// TestIsNonRetryable — the three cancel-worthy errors don't leak
// past the classifier.
func TestIsNonRetryable(t *testing.T) {
	if !isNonRetryable(dbprovider.ErrUnauthorized) {
		t.Error("ErrUnauthorized must be non-retryable")
	}
	if !isNonRetryable(dbprovider.ErrInvalidRequest) {
		t.Error("ErrInvalidRequest must be non-retryable")
	}
	if !isNonRetryable(dbprovider.ErrProviderNotRegistered) {
		t.Error("ErrProviderNotRegistered must be non-retryable")
	}
	if isNonRetryable(dbprovider.ErrRateLimited) {
		t.Error("ErrRateLimited must be retryable — River backs off")
	}
	if isNonRetryable(dbprovider.ErrProviderUnavailable) {
		t.Error("ErrProviderUnavailable must be retryable")
	}
}

// TestProvisionTeamDatabaseWorker_CancelsOnConfigError — non-retryable
// provider errors from Provision return river.JobCancel so the job
// stops after one attempt.
func TestProvisionTeamDatabaseWorker_CancelsOnConfigError(t *testing.T) {
	reg := dbprovider.NewRegistry()
	reg.Register(&mockProvider{
		name: "scaleway",
		provisionFn: func(ctx context.Context, opts dbprovider.ProvisionOpts) (*dbprovider.Instance, error) {
			return nil, dbprovider.ErrUnauthorized
		},
	})
	cipher, _ := dbprovider.NewCipher(testCipherKey, 1)
	// Repo is unused on this path (Provision fails before the row
	// is inserted). nil pool is safe as long as no method is called.
	w := &ProvisionTeamDatabaseWorker{
		Registry: reg,
		Cipher:   cipher,
		Repo:     dbprovider.NewRepo(nil),
	}
	err := w.Work(context.Background(), stubJob(jobs.ProvisionTeamDatabaseArgs{
		ProjectID: "p", Slug: "s", Provider: "scaleway", Region: "fr-par",
	}, 42))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var cancelErr *river.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Errorf("want river.JobCancelError, got %T: %v", err, err)
	}
}

// TestProvisionTeamDatabaseWorker_CancelsOnUnknownProvider — a stray
// provider name never touches Provision.
func TestProvisionTeamDatabaseWorker_CancelsOnUnknownProvider(t *testing.T) {
	cipher, _ := dbprovider.NewCipher(testCipherKey, 1)
	w := &ProvisionTeamDatabaseWorker{
		Registry: dbprovider.NewRegistry(),
		Cipher:   cipher,
		Repo:     dbprovider.NewRepo(nil),
	}
	err := w.Work(context.Background(), stubJob(jobs.ProvisionTeamDatabaseArgs{Provider: "unknown"}, 43))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var cancelErr *river.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Errorf("want river.JobCancelError, got %T: %v", err, err)
	}
}

// TestProvisionTeamDatabaseWorker_CancelsOnMissingCipher — a
// Team-tier job that lands on a worker that booted without
// VAULT_ENCRYPTION_KEY (dev mode allowed this per M1 fix bug_014)
// must cancel the job rather than segfault or silently loop.
func TestProvisionTeamDatabaseWorker_CancelsOnMissingCipher(t *testing.T) {
	reg := dbprovider.NewRegistry()
	reg.Register(&mockProvider{name: "scaleway"})
	w := &ProvisionTeamDatabaseWorker{
		Registry: reg,
		Cipher:   nil, // dev-mode boot, no VAULT_ENCRYPTION_KEY
		Repo:     dbprovider.NewRepo(nil),
	}
	err := w.Work(context.Background(), stubJob(jobs.ProvisionTeamDatabaseArgs{Provider: "scaleway"}, 1))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var cancelErr *river.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Errorf("want river.JobCancelError, got %T: %v", err, err)
	}
}

// TestProvisionTeamDatabaseWorker_Timeout guards against the latent
// bug where River's 1-minute default JobTimeout would guillotine
// pollUntilActive well before Scaleway RDB reaches `ready`. The
// invariant is `Timeout() >= defaultPollTimeout + provisionSlack` —
// a future refactor that raises either constant without touching the
// override, or moves polling under a different constant, fails this
// test rather than silently re-introducing the bug.
func TestProvisionTeamDatabaseWorker_Timeout(t *testing.T) {
	w := &ProvisionTeamDatabaseWorker{}
	got := w.Timeout(stubJob(jobs.ProvisionTeamDatabaseArgs{}, 1))
	want := defaultPollTimeout + provisionSlack
	if got < want {
		t.Fatalf("Timeout() = %v; want >= defaultPollTimeout + provisionSlack (%v) so outer River deadline outlasts pollUntilActive plus surrounding work",
			got, want)
	}
}

const testCipherKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 zero bytes

// testLogger builds a discard-level slog logger for tests so log
// output doesn't clutter -v runs.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
