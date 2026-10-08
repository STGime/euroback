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
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eurobase/euroback/internal/audit"
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
	OnRefusals func(projectID, actorEmail string, count int)

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
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return Entry{ActorID: actorID, ActorEmail: actorEmail, PATID: pat, Via: via, Source: source, IP: ip}
}

// Record writes one entry for a statement, best effort: a request never
// fails or waits long because of its log line.
func (l *Logger) Record(ctx context.Context, projectID, statement string, e Entry) {
	if l == nil || projectID == "" {
		return
	}
	if e.Outcome == OutcomeRefused {
		l.countRefusal(projectID, e.ActorEmail)
	}
	if !l.allow(projectID) {
		return
	}
	sum := sha256.Sum256([]byte(statement))
	// Postgres text can't hold NUL; escape it so such a statement is still
	// logged (the hash and length are of the original).
	stored := strings.ReplaceAll(statement, "\x00", `\0`)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, err := l.pool.Exec(ctx,
		`INSERT INTO public.platform_sql_log
		   (project_id, actor_id, actor_email, pat_id, via, source, statement, statement_len, sha256,
		    read_only, outcome, detail, duration_ms, row_count, ip)
		 VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, ''), NULLIF($4, '')::uuid, $5, $6, $7, $8, $9,
		         $10, $11, NULLIF($12, ''), $13, $14, NULLIF($15, ''))`,
		projectID, e.ActorID, truncate(e.ActorEmail, 300), e.PATID, e.Via, e.Source,
		truncate(stored, MaxStatementLen), len(statement), hex.EncodeToString(sum[:]),
		e.ReadOnly, e.Outcome, truncate(e.Detail, maxDetailLen), e.DurationMs, e.RowCount, truncate(e.IP, 90))
	if err != nil {
		slog.Warn("sql log write failed", "project_id", projectID, "error", err)
	}
}

// allow spends one entry of the project's write budget.
func (l *Logger) allow(projectID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.limiters[projectID]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(logRatePerSecond), logBurst)
		l.limiters[projectID] = lim
	}
	if lim.Allow() {
		return true
	}
	if time.Since(l.dropped[projectID]) > time.Minute {
		l.dropped[projectID] = time.Now()
		slog.Warn("sql log: write budget exceeded, dropping entries", "project_id", projectID)
	}
	return false
}

// countRefusal tracks refused statements per project and calls OnRefusals
// once per window when the threshold is reached. Counted before the write
// budget, so dropped entries still count.
func (l *Logger) countRefusal(projectID, actorEmail string) {
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
			go hook(projectID, actorEmail, count)
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
	rows, err := pool.Query(ctx,
		`SELECT `+listColumns+`
		 FROM public.platform_sql_log
		 WHERE ($1 = '' OR project_id = NULLIF($1, '')::uuid)
		   AND ($2 = '' OR outcome = $2)
		   AND ($3 = 0 OR id < $3)
		 ORDER BY id DESC
		 LIMIT $4`, projectID, o.Outcome, o.BeforeID, o.Limit)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
