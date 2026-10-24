package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/riverqueue/river"
)

type fakeQueue struct {
	mu     sync.Mutex
	params []river.InsertManyParams
	err    error
	e      *Emitter // waited for before reading (processing is async)
}

func (q *fakeQueue) wait() {
	if q.e != nil {
		_ = q.e.Wait(context.Background())
	}
}

func (q *fakeQueue) insert(_ context.Context, p []river.InsertManyParams) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.params = append(q.params, p...)
	return nil
}

func (q *fakeQueue) webhookJobs() []jobs.DeliverProjectWebhookArgs {
	q.wait()
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []jobs.DeliverProjectWebhookArgs
	for _, p := range q.params {
		if a, ok := p.Args.(jobs.DeliverProjectWebhookArgs); ok {
			out = append(out, a)
		}
	}
	return out
}

func (q *fakeQueue) triggerJobs() []jobs.InvokeFunctionTriggerArgs {
	q.wait()
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []jobs.InvokeFunctionTriggerArgs
	for _, p := range q.params {
		if a, ok := p.Args.(jobs.InvokeFunctionTriggerArgs); ok {
			out = append(out, a)
		}
	}
	return out
}

// testEmitter: subscriptions come from subs (per project), loads are
// counted, time is controlled by *now.
func testEmitter(subs map[string]subscriptions, loads *int, now *time.Time) (*Emitter, *fakeQueue) {
	q := &fakeQueue{}
	e := NewEmitter(nil, q.insert)
	q.e = e
	e.now = func() time.Time { return *now }
	var mu sync.Mutex
	e.loadSubs = func(_ context.Context, projectID string) ([]webhookSub, []triggerSub, error) {
		mu.Lock()
		*loads++
		mu.Unlock()
		s := subs[projectID]
		return s.webhooks, s.triggers, nil
	}
	return e, q
}

func TestEmitMatchesWebhooksAndTriggers(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	loads := 0
	e, q := testEmitter(map[string]subscriptions{
		"p1": {
			webhooks: []webhookSub{
				{id: "wh-db", events: []string{DBInsert, DBDelete}},
				{id: "wh-storage", events: []string{StorageUpload}},
			},
			triggers: []triggerSub{
				{id: "tr-todos", table: "todos", events: []string{"INSERT"}},
				{id: "tr-notes", table: "notes", events: []string{"INSERT", "UPDATE"}},
			},
		},
	}, &loads, &now)
	ctx := WithEmitter(context.Background(), e)

	Emit(ctx, Event{ProjectID: "p1", Type: DBInsert, Table: "todos", Data: map[string]any{"id": "r1", "title": "x"}})
	hooks, trigs := q.webhookJobs(), q.triggerJobs()
	if len(hooks) != 1 || hooks[0].WebhookID != "wh-db" || hooks[0].EventType != DBInsert {
		t.Fatalf("webhook jobs = %+v, want one for wh-db", hooks)
	}
	if len(trigs) != 1 || trigs[0].TriggerID != "tr-todos" || trigs[0].EventID != hooks[0].EventID {
		t.Fatalf("trigger jobs = %+v, want tr-todos with the same event id", trigs)
	}
	var env Envelope
	if err := json.Unmarshal(hooks[0].Body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != DBInsert || env.Table != "todos" || env.ProjectID != "p1" || env.Data["title"] != "x" || env.ID != hooks[0].EventID || !env.CreatedAt.Equal(now) {
		t.Fatalf("envelope = %+v", env)
	}

	// An update matches neither webhook, but the notes trigger only on notes.
	Emit(ctx, Event{ProjectID: "p1", Type: DBUpdate, Table: "todos", Data: map[string]any{"id": "r1"}})
	Emit(ctx, Event{ProjectID: "p1", Type: DBUpdate, Table: "notes", Data: map[string]any{"id": "n1"}})
	if hooks, trigs := q.webhookJobs(), q.triggerJobs(); len(hooks) != 1 || len(trigs) != 2 || trigs[1].TriggerID != "tr-notes" {
		t.Fatalf("after updates: hooks %d, triggers %+v", len(hooks), trigs)
	}

	// Storage events reach the storage webhook and never a trigger.
	Emit(ctx, Event{ProjectID: "p1", Type: StorageUpload, Table: "todos", Data: map[string]any{"key": "a.png"}})
	if hooks, trigs := q.webhookJobs(), q.triggerJobs(); len(hooks) != 2 || hooks[1].WebhookID != "wh-storage" || len(trigs) != 2 {
		t.Fatalf("after storage event: hooks %+v, triggers %d", hooks, len(trigs))
	}

	// Another project with no subscriptions: nothing queued.
	Emit(ctx, Event{ProjectID: "p2", Type: DBInsert, Table: "todos"})
	if n := len(q.webhookJobs()) + len(q.triggerJobs()); n != 4 {
		t.Fatalf("jobs = %d after an unsubscribed project's event, want 4", n)
	}
}

func TestEmitCachesSubscriptions(t *testing.T) {
	now := time.Now()
	loads := 0
	subs := map[string]subscriptions{"p1": {webhooks: []webhookSub{{id: "wh", events: []string{DBInsert}}}}}
	e, q := testEmitter(subs, &loads, &now)
	ctx := WithEmitter(context.Background(), e)
	for range 5 {
		Emit(ctx, Event{ProjectID: "p1", Type: DBInsert, Table: "t"})
		_ = e.Wait(ctx)
	}
	if loads != 1 || len(q.webhookJobs()) != 5 {
		t.Fatalf("loads = %d, jobs = %d; want 1 load, 5 jobs", loads, len(q.webhookJobs()))
	}
	// After the TTL a webhook deleted meanwhile stops matching.
	subs["p1"] = subscriptions{}
	now = now.Add(DefaultCacheTTL + time.Second)
	Emit(ctx, Event{ProjectID: "p1", Type: DBInsert, Table: "t"})
	_ = e.Wait(ctx)
	if loads != 2 || len(q.webhookJobs()) != 5 {
		t.Fatalf("after TTL: loads = %d, jobs = %d; want 2 loads, still 5 jobs", loads, len(q.webhookJobs()))
	}
}

func TestEmitKeepsStaleSubscriptionsOnReloadError(t *testing.T) {
	now := time.Now()
	e, q := testEmitter(nil, new(int), &now)
	calls := 0
	e.loadSubs = func(context.Context, string) ([]webhookSub, []triggerSub, error) {
		calls++
		if calls > 1 {
			return nil, nil, errors.New("db down")
		}
		return []webhookSub{{id: "wh", events: []string{DBInsert}}}, nil, nil
	}
	ctx := WithEmitter(context.Background(), e)
	Emit(ctx, Event{ProjectID: "p1", Type: DBInsert, Table: "t"})
	_ = e.Wait(ctx)
	now = now.Add(DefaultCacheTTL + time.Second)
	Emit(ctx, Event{ProjectID: "p1", Type: DBInsert, Table: "t"})
	if len(q.webhookJobs()) != 2 {
		t.Fatalf("jobs = %d, want 2 (stale subscriptions used while the reload fails)", len(q.webhookJobs()))
	}
	// A project never loaded and the DB down: dropped, no panic.
	Emit(ctx, Event{ProjectID: "p2", Type: DBInsert, Table: "t"})
	_ = e.Wait(ctx)
}

func TestEmitTruncatesLargeData(t *testing.T) {
	now := time.Now()
	e, q := testEmitter(map[string]subscriptions{"p1": {webhooks: []webhookSub{{id: "wh", events: []string{DBInsert}}}}}, new(int), &now)
	big := strings.Repeat("x", MaxDataBytes+1)
	Emit(WithEmitter(context.Background(), e), Event{ProjectID: "p1", Type: DBInsert, Table: "t", Data: map[string]any{"id": 7, "body": big}})
	var env Envelope
	if err := json.Unmarshal(q.webhookJobs()[0].Body, &env); err != nil {
		t.Fatal(err)
	}
	if !env.Truncated || len(env.Data) != 1 || env.Data["id"] != float64(7) {
		t.Fatalf("envelope data = %v truncated = %v; want only the id", env.Data, env.Truncated)
	}
}

func TestEmitRateCap(t *testing.T) {
	now := time.Now()
	e, q := testEmitter(map[string]subscriptions{"p1": {webhooks: []webhookSub{{id: "wh", events: []string{DBDelete}}}}}, new(int), &now)
	evs := make([]Event, DefaultBurst+50)
	for i := range evs {
		evs[i] = Event{ProjectID: "p1", Type: DBDelete, Table: "t", Data: map[string]any{"id": i}}
	}
	e.EmitMany(context.Background(), evs)
	if n := len(q.webhookJobs()); n != DefaultBurst {
		t.Fatalf("jobs = %d, want the burst (%d)", n, DefaultBurst)
	}
	_ = e.Wait(context.Background())
	now = now.Add(time.Second)
	e.EmitMany(context.Background(), evs[:10])
	if n := len(q.webhookJobs()); n != DefaultBurst+10 {
		t.Fatalf("jobs = %d one second later, want %d", n, DefaultBurst+10)
	}
}

func TestEmitNilAndEnqueueErrorAreSafe(t *testing.T) {
	Emit(context.Background(), Event{ProjectID: "p1", Type: DBInsert})
	var e *Emitter
	e.EmitMany(context.Background(), []Event{{ProjectID: "p1", Type: DBInsert}})

	now := time.Now()
	e2, q := testEmitter(map[string]subscriptions{"p1": {webhooks: []webhookSub{{id: "wh", events: []string{DBInsert}}}}}, new(int), &now)
	q.err = errors.New("queue down")
	Emit(WithEmitter(context.Background(), e2), Event{ProjectID: "p1", Type: DBInsert, Table: "t"})
	_ = e2.Wait(context.Background())
}

func TestEmitConcurrent(t *testing.T) {
	now := time.Now()
	var nowMu sync.Mutex
	e, q := testEmitter(map[string]subscriptions{"p1": {webhooks: []webhookSub{{id: "wh", events: []string{DBInsert}}}}}, new(int), &now)
	e.now = func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); now = now.Add(time.Second); return now }
	ctx := WithEmitter(context.Background(), e)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				Emit(ctx, Event{ProjectID: "p1", Type: DBInsert, Table: "t"})
			}
		}()
	}
	wg.Wait()
	if n := len(q.webhookJobs()); n != 400 {
		t.Fatalf("jobs = %d, want 400", n)
	}
}

func TestValidType(t *testing.T) {
	for _, ty := range Types {
		if !ValidType(ty) {
			t.Errorf("%s not valid", ty)
		}
	}
	if ValidType("db.*") || ValidType("auth.signin") {
		t.Error("unknown types accepted")
	}
}

// A write's response doesn't wait for the jobs database (#848 review):
// EmitMany returns while the insert is stuck, and a full queue drops
// instead of blocking.
func TestEmitDoesNotBlockOnTheJobsDatabase(t *testing.T) {
	now := time.Now()
	e, _ := testEmitter(map[string]subscriptions{"p1": {webhooks: []webhookSub{{id: "wh", events: []string{DBInsert}}}}}, new(int), &now)
	release := make(chan struct{})
	var inserts atomic.Int32
	e.insert = func(ctx context.Context, p []river.InsertManyParams) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		inserts.Add(1)
		return nil
	}
	e.enqueueAfter = time.Minute
	e.workers = 1
	e.queue = make(chan emitBatch, 2)

	start := time.Now()
	// One batch stuck in the worker, up to two queued, the rest dropped —
	// none of the calls blocks.
	for range 10 {
		e.EmitMany(context.Background(), []Event{{ProjectID: "p1", Type: DBInsert, Table: "t"}})
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("EmitMany blocked for %s", d)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if n := inserts.Load(); n < 2 || n > 3 {
		t.Fatalf("inserts = %d, want 2 or 3 (the rest dropped)", n)
	}
}

// A project the cache knows has no subscriptions never reaches the queue;
// once the cache entry is stale (or invalidated) it goes through again.
func TestEmitSkipsKnownUnsubscribedProjects(t *testing.T) {
	now := time.Now()
	loads := 0
	e, _ := testEmitter(map[string]subscriptions{}, &loads, &now)
	if e.knownUnsubscribed("p1") {
		t.Fatal("unknown project treated as unsubscribed")
	}
	e.EmitMany(context.Background(), []Event{{ProjectID: "p1", Type: DBInsert}})
	_ = e.Wait(context.Background())
	if !e.knownUnsubscribed("p1") {
		t.Fatal("loaded project without subscriptions not on the fast path")
	}
	e.Invalidate("p1")
	if e.knownUnsubscribed("p1") {
		t.Fatal("invalidated project still on the fast path")
	}
	e.EmitMany(context.Background(), []Event{{ProjectID: "p1", Type: DBInsert}})
	_ = e.Wait(context.Background())
	now = now.Add(DefaultCacheTTL + time.Second)
	if e.knownUnsubscribed("p1") {
		t.Fatal("stale cache entry still on the fast path")
	}
	if loads != 2 {
		t.Fatalf("loads = %d, want 2", loads)
	}
}
