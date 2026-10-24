// Package events turns writes made through the gateway into deliveries
// for a project's webhooks and function triggers (#845).
//
// Handlers call Emit after a write has committed; the Emitter matches
// the event against the project's enabled webhooks (public.webhooks) and
// function triggers (public.function_triggers) and enqueues one River
// job per match on the project_events queue. The worker delivers them
// (internal/workers: DeliverProjectWebhookWorker,
// InvokeFunctionTriggerWorker).
//
// Events come from the gateway's own write paths: the data API (SDK and
// console Table Editor), storage uploads and deletes through the
// gateway, and end-user accounts created or deleted through the auth
// API. Writes made with SQL (SQL editor, /v1/db/sql, RPC functions,
// edge-function SQL, cron SQL) and direct-to-S3 uploads with a signed
// URL don't pass through these paths and emit nothing.
//
// The Emitter travels in the request context (WithEmitter / Middleware,
// like audit.AccessRecorderFromContext), so the handlers that emit need
// no extra constructor arguments. A nil *Emitter is a no-op.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"golang.org/x/time/rate"
)

// Event types a webhook can subscribe to. The console offers exactly
// these (console/src/routes/(app)/p/[id]/webhooks/+page.svelte).
const (
	DBInsert        = "db.insert"
	DBUpdate        = "db.update"
	DBDelete        = "db.delete"
	StorageUpload   = "storage.upload"
	StorageDelete   = "storage.delete"
	AuthUserCreated = "auth.user.created"
	AuthUserDeleted = "auth.user.deleted"
)

// Types lists every event type, in the console's order.
var Types = []string{DBInsert, DBUpdate, DBDelete, StorageUpload, StorageDelete, AuthUserCreated, AuthUserDeleted}

// ValidType reports whether t is an event type a webhook can subscribe to.
func ValidType(t string) bool { return slices.Contains(Types, t) }

// triggerOp maps a db.* event to the operation name function triggers
// store (INSERT / UPDATE / DELETE); "" for other events.
func triggerOp(eventType string) string {
	switch eventType {
	case DBInsert:
		return "INSERT"
	case DBUpdate:
		return "UPDATE"
	case DBDelete:
		return "DELETE"
	}
	return ""
}

// Event is one change in a project.
type Event struct {
	ProjectID string
	Type      string
	// Table is the table a db.* event happened on; empty otherwise.
	Table string
	// Data is the event's payload: the row as the data API returned it
	// (sensitive system-table columns removed) for db.insert / db.update,
	// {"id": …} for db.delete, the object for storage events, the user
	// for auth events.
	Data map[string]any
}

// Envelope is the JSON body a webhook endpoint receives and a triggered
// function gets as its request body. `type` and `data` are the fields
// the webhooks docs (chapter 11) show a receiver reading.
type Envelope struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	CreatedAt time.Time      `json:"created_at"`
	ProjectID string         `json:"project_id"`
	Table     string         `json:"table,omitempty"`
	Data      map[string]any `json:"data"`
	// Truncated: Data was larger than MaxDataBytes and was cut down to
	// its "id" (or "key"); read the current record from the API.
	Truncated bool `json:"truncated,omitempty"`
}

// MaxDataBytes caps an event's encoded data. Larger rows are sent with
// only their id, so one wide row can't bloat the job queue.
const MaxDataBytes = 64 << 10

// Defaults for NewEmitter.
const (
	// DefaultCacheTTL: how long a project's subscriptions are cached. A
	// new or changed webhook takes effect within this window.
	DefaultCacheTTL = 15 * time.Second
	// DefaultRate / DefaultBurst cap events per project per gateway pod.
	// The burst fits one bulk delete (1000 rows); beyond the cap events
	// are dropped and logged rather than queued without bound.
	DefaultRate  = 100
	DefaultBurst = 1000
)

// InsertFunc enqueues River jobs (a River client's InsertMany).
type InsertFunc func(ctx context.Context, params []river.InsertManyParams) error

// RiverInserter adapts a River client to InsertFunc.
func RiverInserter[TTx any](c *river.Client[TTx]) InsertFunc {
	return func(ctx context.Context, params []river.InsertManyParams) error {
		_, err := c.InsertMany(ctx, params)
		return err
	}
}

type webhookSub struct {
	id     string
	events []string
}

type triggerSub struct {
	id     string
	table  string
	events []string
}

// subscriptions is a project's matching set. Never modified after it is
// built: a reload swaps in a new one.
type subscriptions struct {
	webhooks []webhookSub
	triggers []triggerSub
}

type projectState struct {
	subs     *subscriptions
	loadedAt time.Time
	limiter  *rate.Limiter
	// droppedLoggedAt rate-limits the "events dropped" warning.
	droppedLoggedAt time.Time
}

// Emitter matches events against subscriptions and enqueues deliveries.
type Emitter struct {
	pool   *pgxpool.Pool
	insert InsertFunc

	ttl          time.Duration
	limit        rate.Limit
	burst        int
	now          func() time.Time
	loadSubs     func(ctx context.Context, projectID string) ([]webhookSub, []triggerSub, error)
	enqueueAfter time.Duration

	mu       sync.Mutex
	projects map[string]*projectState
}

// NewEmitter returns an Emitter that reads subscriptions with pool (the
// gateway pool can read public.webhooks and public.function_triggers)
// and enqueues with insert.
func NewEmitter(pool *pgxpool.Pool, insert InsertFunc) *Emitter {
	e := &Emitter{
		pool:         pool,
		insert:       insert,
		ttl:          DefaultCacheTTL,
		limit:        DefaultRate,
		burst:        DefaultBurst,
		now:          time.Now,
		enqueueAfter: 3 * time.Second,
		projects:     map[string]*projectState{},
	}
	e.loadSubs = e.loadFromDB
	return e
}

type ctxKey struct{}

// WithEmitter returns ctx carrying e.
func WithEmitter(ctx context.Context, e *Emitter) context.Context {
	if e == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, e)
}

// FromContext returns the Emitter in ctx, or nil (a no-op Emitter).
func FromContext(ctx context.Context) *Emitter {
	e, _ := ctx.Value(ctxKey{}).(*Emitter)
	return e
}

// Middleware puts e in every request's context.
func Middleware(e *Emitter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if e == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithEmitter(r.Context(), e)))
		})
	}
}

// Emit delivers one event. See EmitMany.
func Emit(ctx context.Context, ev Event) { FromContext(ctx).EmitMany(ctx, []Event{ev}) }

// EmitMany matches events (all of one project) against the project's
// subscriptions and enqueues a delivery per match. Call it after the
// change has committed. It never fails the caller: the write already
// happened, so problems are logged. Unsubscribed projects cost one
// cached lookup and no database round trip.
func (e *Emitter) EmitMany(ctx context.Context, evs []Event) {
	if e == nil || len(evs) == 0 {
		return
	}
	projectID := evs[0].ProjectID
	if projectID == "" {
		return
	}
	st, subs, err := e.state(ctx, projectID)
	if err != nil {
		slog.Error("events: load subscriptions failed — events dropped",
			"project_id", projectID, "events", len(evs), "error", err)
		return
	}

	now := e.now()
	var params []river.InsertManyParams
	dropped := 0
	for _, ev := range evs {
		if ev.ProjectID != projectID {
			continue
		}
		hooks, trigs := subs.match(ev)
		if len(hooks) == 0 && len(trigs) == 0 {
			continue
		}
		if !e.allow(st, now) {
			dropped++
			continue
		}
		env, body, err := encode(ev, now)
		if err != nil {
			slog.Error("events: encode event", "project_id", projectID, "type", ev.Type, "error", err)
			continue
		}
		for _, id := range hooks {
			params = append(params, river.InsertManyParams{Args: jobs.DeliverProjectWebhookArgs{
				WebhookID: id, EventID: env.ID, EventType: env.Type, Body: body,
			}})
		}
		for _, id := range trigs {
			params = append(params, river.InsertManyParams{Args: jobs.InvokeFunctionTriggerArgs{
				TriggerID: id, EventID: env.ID, Body: body,
			}})
		}
	}
	if dropped > 0 {
		e.logDropped(st, projectID, dropped, now)
	}
	if len(params) == 0 {
		return
	}
	// The write has committed: don't let a client disconnect cancel the
	// enqueue, but don't hold the response for long either.
	ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.enqueueAfter)
	defer cancel()
	if err := e.insert(ictx, params); err != nil {
		slog.Error("events: enqueue deliveries failed — events dropped",
			"project_id", projectID, "jobs", len(params), "error", err)
	}
}

// match returns the webhook and trigger IDs subscribed to ev.
func (subs *subscriptions) match(ev Event) (hooks, trigs []string) {
	for _, h := range subs.webhooks {
		if slices.Contains(h.events, ev.Type) {
			hooks = append(hooks, h.id)
		}
	}
	if op := triggerOp(ev.Type); op != "" && ev.Table != "" {
		for _, t := range subs.triggers {
			if t.table == ev.Table && slices.Contains(t.events, op) {
				trigs = append(trigs, t.id)
			}
		}
	}
	return hooks, trigs
}

func encode(ev Event, now time.Time) (Envelope, json.RawMessage, error) {
	env := Envelope{
		ID:        uuid.NewString(),
		Type:      ev.Type,
		CreatedAt: now.UTC(),
		ProjectID: ev.ProjectID,
		Table:     ev.Table,
		Data:      ev.Data,
	}
	if env.Data == nil {
		env.Data = map[string]any{}
	}
	data, err := json.Marshal(env.Data)
	if err != nil {
		return env, nil, err
	}
	if len(data) > MaxDataBytes {
		small := map[string]any{}
		for _, k := range []string{"id", "key"} {
			if v, ok := env.Data[k]; ok {
				small[k] = v
			}
		}
		env.Data, env.Truncated = small, true
	}
	body, err := json.Marshal(env)
	return env, body, err
}

// allow takes one token from the project's limiter (safe for
// concurrent use on its own).
func (e *Emitter) allow(st *projectState, now time.Time) bool {
	return st.limiter.AllowN(now, 1)
}

func (e *Emitter) logDropped(st *projectState, projectID string, n int, now time.Time) {
	e.mu.Lock()
	log := now.Sub(st.droppedLoggedAt) >= time.Minute
	if log {
		st.droppedLoggedAt = now
	}
	e.mu.Unlock()
	if log {
		slog.Warn("events: project over its event rate — events dropped",
			"project_id", projectID, "dropped", n, "rate_per_s", float64(e.limit), "burst", e.burst)
	}
}

// state returns the project's subscriptions, reloading them when older
// than the TTL. On a reload error a stale copy is kept for one more TTL
// rather than dropping events.
func (e *Emitter) state(ctx context.Context, projectID string) (*projectState, *subscriptions, error) {
	now := e.now()
	e.mu.Lock()
	st := e.projects[projectID]
	if st != nil && now.Sub(st.loadedAt) < e.ttl {
		subs := st.subs
		e.mu.Unlock()
		return st, subs, nil
	}
	e.mu.Unlock()

	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	hooks, trigs, err := e.loadSubs(lctx, projectID)

	e.mu.Lock()
	defer e.mu.Unlock()
	st = e.projects[projectID]
	if err != nil {
		if st != nil {
			st.loadedAt = now
			return st, st.subs, nil
		}
		return nil, nil, err
	}
	if st == nil {
		if len(e.projects) >= 50_000 {
			e.pruneLocked(now)
		}
		st = &projectState{limiter: rate.NewLimiter(e.limit, e.burst)}
		e.projects[projectID] = st
	}
	st.subs, st.loadedAt = &subscriptions{webhooks: hooks, triggers: trigs}, now
	return st, st.subs, nil
}

// Invalidate drops the project's cached subscriptions, so a webhook or
// function trigger saved on this pod applies to the next event. Other
// pods pick it up within the cache TTL.
func (e *Emitter) Invalidate(projectID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if st := e.projects[projectID]; st != nil {
		st.loadedAt = time.Time{}
	}
}

// pruneLocked drops cached projects idle for several TTLs.
func (e *Emitter) pruneLocked(now time.Time) {
	for id, st := range e.projects {
		if now.Sub(st.loadedAt) > 4*e.ttl {
			delete(e.projects, id)
		}
	}
}

func (e *Emitter) loadFromDB(ctx context.Context, projectID string) ([]webhookSub, []triggerSub, error) {
	if e.pool == nil {
		return nil, nil, errors.New("no database pool")
	}
	var hooks []webhookSub
	rows, err := e.pool.Query(ctx,
		`SELECT id::text, events FROM public.webhooks
		  WHERE project_id = $1 AND enabled IS NOT FALSE AND cardinality(events) > 0`, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("load webhooks: %w", err)
	}
	for rows.Next() {
		var h webhookSub
		if err := rows.Scan(&h.id, &h.events); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan webhook: %w", err)
		}
		hooks = append(hooks, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("load webhooks: %w", err)
	}

	var trigs []triggerSub
	rows, err = e.pool.Query(ctx,
		`SELECT t.id::text, t.table_name, t.events
		   FROM public.function_triggers t
		   JOIN public.edge_functions f ON f.id = t.function_id
		  WHERE t.project_id = $1 AND t.enabled IS NOT FALSE AND f.status = 'active'`, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("load function triggers: %w", err)
	}
	for rows.Next() {
		var t triggerSub
		if err := rows.Scan(&t.id, &t.table, &t.events); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan function trigger: %w", err)
		}
		trigs = append(trigs, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("load function triggers: %w", err)
	}
	return hooks, trigs, nil
}
