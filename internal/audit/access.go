package audit

// Personal-data access logging — Tier-1 GDPR #4 (Art. 30 / Art. 32).
//
// public.audit_log records sensitive *admin* actions. It does NOT record who
// *read* tenant personal data over the SDK. AccessRecorder fills that gap by
// writing one row per personal-data read / export / download to
// public.data_access_log (migration 000066).
//
// Design constraints:
//   - MUST NOT block the request path. The /v1/db read path is hot; an audit
//     write that took a DB round-trip inline would show up on p99. So Record()
//     only does a non-blocking channel send and returns; a single background
//     worker batches the inserts.
//   - Best-effort. If the buffer is full (DB slow / burst), events are dropped
//     and counted, never queued unbounded and never blocking. A dropped read
//     log is acceptable; a stalled request is not.
//   - Sampling. Writes/exports/downloads are always logged; reads can be
//     sampled (configurable) for very high-traffic projects.

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Access action constants stored in data_access_log.action.
const (
	AccessActionRead     = "read"     // a tenant-table read (sampled)
	AccessActionExport   = "export"   // a DSAR / bulk personal-data export (always)
	AccessActionDownload = "download" // a storage object download (always)
)

// Effective RLS roles recorded in data_access_log.actor_role.
const (
	roleAuthenticated = "authenticated"
	roleService       = "service"
	roleAnon          = "anon"
)

// AccessEvent is one personal-data access. All fields are plain strings so the
// hot path never allocates beyond the optional TargetKeys map.
type AccessEvent struct {
	ProjectID   string                 // project UUID (empty allowed → NULL)
	EndUserID   string                 // data subject / caller UUID (empty → NULL)
	ActorRole   string                 // see role* constants, or "platform"
	Action      string                 // see AccessAction* constants
	TargetTable string                 // tenant table, or "storage_objects"
	TargetKeys  map[string]interface{} // which rows/keys (e.g. {"id":...}, {"filters":...})
	IP          string                 // client IP (empty → NULL)
}

// AccessRecorderConfig tunes the recorder. Zero value is not usable; build from
// DefaultAccessRecorderConfig() and override.
type AccessRecorderConfig struct {
	Enabled       bool
	ReadSample    float64       // P(log) for action=read, in [0,1]. 1 = log all.
	BufferSize    int           // channel capacity; events beyond this are dropped
	BatchSize     int           // flush when this many events are buffered
	FlushInterval time.Duration // flush at least this often
}

// DefaultAccessRecorderConfig returns sane production defaults: log every
// access (full compliance coverage), with a generous buffer so bursts don't
// drop, flushed every 2s or every 256 events.
func DefaultAccessRecorderConfig() AccessRecorderConfig {
	return AccessRecorderConfig{
		Enabled:       true,
		ReadSample:    1.0,
		BufferSize:    8192,
		BatchSize:     256,
		FlushInterval: 2 * time.Second,
	}
}

// AccessRecorder is a fire-and-forget, sampled, batched writer for
// data_access_log. Construct with NewAccessRecorder (which starts the worker)
// and Close on shutdown to flush. A nil *AccessRecorder is safe: every method
// is a no-op, so callers don't have to nil-check.
type AccessRecorder struct {
	pool *pgxpool.Pool
	cfg  AccessRecorderConfig

	ch        chan AccessEvent
	wg        sync.WaitGroup
	closeOnce sync.Once

	dropped     int64 // atomic: events dropped because the buffer was full
	lastDropLog int64 // atomic: value of dropped at last warning, to avoid log spam
}

// NewAccessRecorder starts the background worker and returns the recorder. If
// cfg.Enabled is false it returns a recorder whose Record is a no-op and whose
// worker never starts.
func NewAccessRecorder(pool *pgxpool.Pool, cfg AccessRecorderConfig) *AccessRecorder {
	if !cfg.Enabled || pool == nil {
		return &AccessRecorder{cfg: AccessRecorderConfig{Enabled: false}}
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 8192
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 256
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 2 * time.Second
	}
	r := &AccessRecorder{
		pool: pool,
		cfg:  cfg,
		ch:   make(chan AccessEvent, cfg.BufferSize),
	}
	r.wg.Add(1)
	go r.run()
	slog.Info("data access logging enabled",
		"read_sample", cfg.ReadSample, "buffer", cfg.BufferSize,
		"batch", cfg.BatchSize, "flush_interval", cfg.FlushInterval)
	return r
}

// Record enqueues an access event. It never blocks: if the buffer is full the
// event is dropped and counted. Reads are subject to the configured sample
// rate; exports and downloads are always recorded.
func (r *AccessRecorder) Record(ev AccessEvent) {
	if r == nil || !r.cfg.Enabled {
		return
	}
	if ev.Action == AccessActionRead && r.cfg.ReadSample < 1.0 {
		// rand.Float64 is safe for concurrent use (locked global source).
		if r.cfg.ReadSample <= 0 || rand.Float64() >= r.cfg.ReadSample {
			return
		}
	}
	select {
	case r.ch <- ev:
	default:
		atomic.AddInt64(&r.dropped, 1)
	}
}

// Close stops accepting events and flushes what's buffered, bounded by ctx.
// Safe to call multiple times.
func (r *AccessRecorder) Close(ctx context.Context) {
	if r == nil || !r.cfg.Enabled {
		return
	}
	r.closeOnce.Do(func() { close(r.ch) })
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		if d := atomic.LoadInt64(&r.dropped); d > 0 {
			slog.Warn("data access logger drained", "dropped_total", d)
		}
	case <-ctx.Done():
		slog.Warn("data access logger drain timed out", "dropped_total", atomic.LoadInt64(&r.dropped))
	}
}

func (r *AccessRecorder) run() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]AccessEvent, 0, r.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		r.flush(batch)
		batch = batch[:0]
	}

	for {
		select {
		case ev, ok := <-r.ch:
			if !ok {
				flush() // channel closed → drain and exit
				return
			}
			batch = append(batch, ev)
			if len(batch) >= r.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
			r.reportDropped()
		}
	}
}

// flush writes a batch of events to data_access_log in one round-trip. Errors
// are logged, not retried: this is an audit sampler, not transactional data,
// and the off-box WORM dump (#170/#171) is the durable copy.
func (r *AccessRecorder) flush(events []AccessEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	b := &pgx.Batch{}
	for _, ev := range events {
		keys := []byte("{}")
		if ev.TargetKeys != nil {
			if encoded, err := json.Marshal(ev.TargetKeys); err == nil {
				keys = encoded
			}
		}
		b.Queue(
			`INSERT INTO public.data_access_log
			   (project_id, end_user_id, actor_role, action, target_table, target_keys, ip)
			 VALUES (NULLIF($1,'')::uuid, NULLIF($2,'')::uuid, $3, $4, $5, $6::jsonb, NULLIF($7,''))`,
			ev.ProjectID, ev.EndUserID, ev.ActorRole, ev.Action, ev.TargetTable, keys, ev.IP,
		)
	}

	br := r.pool.SendBatch(ctx, b)
	defer br.Close()
	for range events {
		if _, err := br.Exec(); err != nil {
			// A failure aborts the implicit batch transaction, so the rest will
			// error too — log once and stop reading.
			slog.Error("data access log batch insert failed", "batch_size", len(events), "error", err)
			return
		}
	}
}

// reportDropped emits a warning when events have been dropped since the last
// tick, so silent data loss under sustained overload is visible in logs.
func (r *AccessRecorder) reportDropped() {
	total := atomic.LoadInt64(&r.dropped)
	last := atomic.LoadInt64(&r.lastDropLog)
	if total > last {
		slog.Warn("data access log events dropped (buffer full)", "dropped_since_last", total-last, "dropped_total", total)
		atomic.StoreInt64(&r.lastDropLog, total)
	}
}

// EffectiveRole maps the SDK request's key type + end-user identity onto the
// effective RLS role that the read executed under. Mirrors applyRLSContext in
// internal/query/engine.go so the access log says the same thing the database
// enforced.
func EffectiveRole(keyType, endUserID string) string {
	if endUserID != "" {
		return roleAuthenticated
	}
	if keyType == "secret" {
		return roleService
	}
	return roleAnon
}

// ── Context plumbing ──
// The recorder and the resolved client IP are stashed in the request context
// by the gateway so handlers in the query / storage / enduser packages can
// record access without taking new constructor params or importing each other.

type accessRecorderKey struct{}
type clientIPKey struct{}

// WithAccessRecorder stores the recorder in the context.
func WithAccessRecorder(ctx context.Context, rec *AccessRecorder) context.Context {
	return context.WithValue(ctx, accessRecorderKey{}, rec)
}

// AccessRecorderFromContext retrieves the recorder. Returns nil if not set;
// a nil recorder's Record is a safe no-op.
func AccessRecorderFromContext(ctx context.Context) *AccessRecorder {
	rec, _ := ctx.Value(accessRecorderKey{}).(*AccessRecorder)
	return rec
}

// WithClientIP stores the resolved client IP (X-Forwarded-For aware) in context.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIPFromContext retrieves the client IP, or "" if not set.
func ClientIPFromContext(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}
