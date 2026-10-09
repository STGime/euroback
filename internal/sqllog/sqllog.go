// Package sqllog records the SQL developers send through the platform
// (migration 000139): the console SQL editor and the multi-statement
// endpoint (also used by the MCP server and the CLI with a personal access
// token), custom function bodies, custom RLS policy expressions and tenant
// migrations. One row per statement: who sent it and how, the text (capped)
// and its SHA-256, and what happened — ran, failed, or refused by the
// platform's checks before it ran.
//
// The statement text can contain the project's own data, so rows are read
// only on the developer pool (console: project admins) and kept 30 days.
package sqllog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/clientip"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

// How the statement reached the platform.
const (
	ViaConsole = "console" // a console session (password, passkey, SSO)
	ViaToken   = "token"   // a personal access token (MCP server, CLI, scripts)
)

// Which platform surface ran it.
const (
	SourceSQL            = "sql"             // POST …/data/sql
	SourceSQLTransaction = "sql_transaction" // POST …/data/sql/transaction, one row per statement
	SourceFunction       = "function"        // POST …/schema/functions (the generated CREATE FUNCTION)
	SourcePolicy         = "policy"          // custom RLS policy (the generated CREATE POLICY)
	SourceMigration      = "migration"       // POST …/migrations (the migration body)
)

// Outcomes.
const (
	OutcomeOK      = "ok"
	OutcomeError   = "error"   // the database rejected it
	OutcomeRefused = "refused" // the platform's checks refused it before it ran
	// In a multi-statement request: not executed because another statement
	// was refused or failed. Not counted as a refusal.
	OutcomeNotRun = "not_run"
)

// Retention is how long entries are kept.
const Retention = 30 * 24 * time.Hour

// MaxStatementLen caps the stored text; the hash and length cover all of it.
const MaxStatementLen = 8192

const maxDetailLen = 500

// Per-project write budget. A developer can send statements as fast as the
// endpoints allow; past the budget, entries are dropped (and counted in a
// warning) rather than letting one project fill the table. The statement
// itself is unaffected.
const (
	logRatePerSecond = 5
	logBurst         = 300
)

// Refusal alert: this many refused statements for one project inside the
// window calls the alert hook once per window.
const (
	RefusalAlertThreshold = 10
	RefusalAlertWindow    = 10 * time.Minute
)

// Entry is one row of the log.
type Entry struct {
	ID           int64     `json:"id"`
	ProjectID    string    `json:"project_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ActorID      string    `json:"actor_id,omitempty"`
	ActorEmail   string    `json:"actor_email,omitempty"`
	PATID        string    `json:"pat_id,omitempty"`
	Via          string    `json:"via"`
	Source       string    `json:"source"`
	Statement    string    `json:"statement"`
	StatementLen int       `json:"statement_len"`
	SHA256       string    `json:"sha256"`
	ReadOnly     bool      `json:"read_only"`
	Outcome      string    `json:"outcome"`
	Detail       string    `json:"detail,omitempty"`
	DurationMs   *int      `json:"duration_ms,omitempty"`
	RowCount     *int      `json:"row_count,omitempty"`
	IP           string    `json:"ip,omitempty"`
}

// Logger writes entries. It holds the gateway's runtime pool, which may
// only INSERT into the table (000139).
type Logger struct {
	pool execer

	// OnRefusals is called (in its own goroutine) when a project reaches
	// RefusalAlertThreshold refused statements inside RefusalAlertWindow.
	OnRefusals func(projectID string, count int)

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	dropped  map[string]time.Time // last "dropping" warning per project
	refusals map[string]*refusalWindow
}

// execer is what Logger writes through: the gateway pool, or a fake in tests.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// NewWithWriter returns a logger that writes through w (tests).
func NewWithWriter(w execer) *Logger {
	return &Logger{
		pool:     w,
		limiters: map[string]*rate.Limiter{},
		dropped:  map[string]time.Time{},
		refusals: map[string]*refusalWindow{},
	}
}

type refusalWindow struct {
	start   time.Time
	count   int
	alerted bool
}

// New returns a logger; a nil pool gives a no-op logger.
func New(pool *pgxpool.Pool) *Logger {
	if pool == nil {
		return nil
	}
	return NewWithWriter(pool)
}

type ctxKey struct{}

// WithContext stores the logger in the request context (router middleware).
func WithContext(ctx context.Context, l *Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the logger, or nil (a nil *Logger is a no-op).
func FromContext(ctx context.Context) *Logger {
	l, _ := ctx.Value(ctxKey{}).(*Logger)
	return l
}

// FromRequest returns an entry with the caller filled in from the request:
// actor and token (set by the platform auth + audit middleware) and IP.
func FromRequest(r *http.Request, source string) Entry {
	actorID, actorEmail := audit.ActorFromContext(r.Context())
	pat := audit.ActorTokenFromContext(r.Context())
	via := ViaConsole
	if pat != "" {
		via = ViaToken
	}
	// The same trusted-hop rule as the platform auth routes: not a client-
	// supplied forwarding header.
	ip := clientip.FromRequestDefault(r)
	return Entry{ActorID: validUUID(actorID), ActorEmail: actorEmail, PATID: validUUID(pat), Via: via, Source: source, IP: ip}
}

// Item is one statement and its entry, for RecordBatch.
type Item struct {
	Statement string
	Entry     Entry
}

// Record writes one entry for a statement, best effort: a request never
// fails or waits long because of its log line.
func (l *Logger) Record(ctx context.Context, projectID, statement string, e Entry) {
	l.RecordBatch(ctx, projectID, []Item{{Statement: statement, Entry: e}})
}

// RecordBatch writes the entries of one request in a single INSERT (a
// multi-statement request is one round trip, not one per statement).
// Entries past the project's write budget are dropped; refusals are
// counted for the alert before that.
func (l *Logger) RecordBatch(ctx context.Context, projectID string, items []Item) {
	if l == nil || projectID == "" || len(items) == 0 {
		return
	}
	for _, it := range items {
		if it.Entry.Outcome == OutcomeRefused {
			l.countRefusal(projectID)
		}
	}
	items = items[:l.allowN(projectID, len(items))]
	if len(items) == 0 {
		return
	}
	const cols = 15
	var sb strings.Builder
	sb.WriteString(`INSERT INTO public.platform_sql_log
		   (project_id, actor_id, actor_email, pat_id, via, source, statement, statement_len, sha256,
		    read_only, outcome, detail, duration_ms, row_count, ip) VALUES `)
	args := make([]any, 0, cols*len(items))
	for i, it := range items {
		if i > 0 {
			sb.WriteString(", ")
		}
		n := i * cols
		fmt.Fprintf(&sb, "($%d, NULLIF($%d, '')::uuid, NULLIF($%d, ''), NULLIF($%d, '')::uuid, $%d, $%d, $%d, $%d, $%d, $%d, $%d, NULLIF($%d, ''), $%d, $%d, NULLIF($%d, ''))",
			n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10, n+11, n+12, n+13, n+14, n+15)
		e := it.Entry
		sum := sha256.Sum256([]byte(it.Statement))
		// Postgres text can't hold NUL; escape it so such a statement is
		// still logged (the hash and length are of the original).
		stored := strings.ReplaceAll(it.Statement, "\x00", `\0`)
		args = append(args, projectID, validUUID(e.ActorID), truncate(e.ActorEmail, 300), validUUID(e.PATID), e.Via, e.Source,
			truncate(stored, MaxStatementLen), len(it.Statement), hex.EncodeToString(sum[:]),
			e.ReadOnly, e.Outcome, truncate(e.Detail, maxDetailLen), e.DurationMs, e.RowCount, truncate(e.IP, 90))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, err := l.pool.Exec(ctx, sb.String(), args...); err != nil {
		slog.Warn("sql log write failed", "project_id", projectID, "entries", len(items), "error", err)
	}
}

// allowN spends up to n entries of the project's write budget and returns
// how many may be written.
func (l *Logger) allowN(projectID string, n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.limiters[projectID]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(logRatePerSecond), logBurst)
		l.limiters[projectID] = lim
	}
	allowed := 0
	for allowed < n && lim.Allow() {
		allowed++
	}
	if allowed < n && time.Since(l.dropped[projectID]) > time.Minute {
		l.dropped[projectID] = time.Now()
		slog.Warn("sql log: write budget exceeded, dropping entries", "project_id", projectID, "dropped", n-allowed)
	}
	return allowed
}

// countRefusal tracks refused statements per project and calls OnRefusals
// once per window when the threshold is reached. Counted before the write
// budget, so dropped entries still count.
func (l *Logger) countRefusal(projectID string) {
	l.mu.Lock()
	w := l.refusals[projectID]
	now := time.Now()
	if w == nil || now.Sub(w.start) > RefusalAlertWindow {
		w = &refusalWindow{start: now}
		l.refusals[projectID] = w
	}
	w.count++
	fire := !w.alerted && w.count >= RefusalAlertThreshold
	if fire {
		w.alerted = true
	}
	count := w.count
	hook := l.OnRefusals
	l.mu.Unlock()
	if fire {
		slog.Warn("sql log: many refused statements", "project_id", projectID, "count", count, "window", RefusalAlertWindow.String())
		if hook != nil {
			go hook(projectID, count)
		}
	}
}

// ListOptions filters a listing.
type ListOptions struct {
	Outcome  string // "", or one of the outcomes
	BeforeID int64  // keyset pagination: entries with id < BeforeID
	Limit    int    // default 100, max 500
}

const listColumns = `id, project_id, created_at, COALESCE(actor_id::text, ''), COALESCE(actor_email, ''),
	COALESCE(pat_id::text, ''), via, source, statement, statement_len, sha256, read_only, outcome,
	COALESCE(detail, ''), duration_ms, row_count, COALESCE(ip, '')`

// List returns a project's entries, newest first. Developer pool (the
// runtime role can't read the table).
func List(ctx context.Context, pool *pgxpool.Pool, projectID string, o ListOptions) ([]Entry, error) {
	return list(ctx, pool, projectID, o)
}

// ListAll returns entries across all projects, newest first (superadmin).
func ListAll(ctx context.Context, pool *pgxpool.Pool, o ListOptions) ([]Entry, error) {
	return list(ctx, pool, "", o)
}

func list(ctx context.Context, pool *pgxpool.Pool, projectID string, o ListOptions) ([]Entry, error) {
	if o.Limit <= 0 || o.Limit > 500 {
		o.Limit = 100
	}
	// Built per filter (no "$1 = '' OR …"), so each shape uses its index:
	// (project_id, id DESC), or the partial refused index across projects.
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if projectID != "" {
		add("project_id = $%d", projectID)
	}
	if o.Outcome != "" {
		add("outcome = $%d", o.Outcome)
	}
	if o.BeforeID > 0 {
		add("id < $%d", o.BeforeID)
	}
	q := `SELECT ` + listColumns + ` FROM public.platform_sql_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, o.Limit)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.CreatedAt, &e.ActorID, &e.ActorEmail, &e.PATID, &e.Via, &e.Source,
			&e.Statement, &e.StatementLen, &e.SHA256, &e.ReadOnly, &e.Outcome, &e.Detail, &e.DurationMs, &e.RowCount, &e.IP); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Cleanup deletes entries older than Retention, in batches. Developer pool.
func Cleanup(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var total int64
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		tag, err := pool.Exec(batchCtx,
			`DELETE FROM public.platform_sql_log WHERE id IN (
			   SELECT id FROM public.platform_sql_log
			   WHERE created_at < now() - make_interval(secs => $1)
			   LIMIT 10000)`,
			Retention.Seconds())
		cancel()
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 10000 {
			return total, nil
		}
	}
}

// StartCleanup runs Cleanup hourly until ctx ends. Developer pool.
func StartCleanup(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if n, err := Cleanup(ctx, pool); err != nil {
				slog.Error("failed to clean up the SQL log", "error", err)
			} else if n > 0 {
				slog.Info("cleaned up the SQL log", "deleted", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Ms converts a duration to whole milliseconds for Entry.DurationMs.
func Ms(d time.Duration) *int {
	ms := int(d.Milliseconds())
	return &ms
}

// Int returns a pointer, for Entry.RowCount.
func Int(n int) *int { return &n }

// validUUID returns s if it's a UUID, else "" (stored as NULL) — a
// malformed id must not make the whole insert fail.
func validUUID(s string) string {
	if _, err := uuid.Parse(s); err != nil {
		return ""
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
