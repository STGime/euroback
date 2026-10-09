package sqllog

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeWriter records the arguments of each INSERT.
type fakeWriter struct {
	mu   sync.Mutex
	rows [][]any
}

func (f *fakeWriter) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, args)
	return pgconn.CommandTag{}, nil
}

func (f *fakeWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

// Insert argument positions (see Record).
const (
	argProject = iota
	argActorID
	argActorEmail
	argPAT
	argVia
	argSource
	argStatement
	argLen
	argSHA
	argReadOnly
	argOutcome
	argDetail
)

func TestRecord_HashLengthAndTruncation(t *testing.T) {
	w := &fakeWriter{}
	l := NewWithWriter(w)
	long := strings.Repeat("é", MaxStatementLen) // 2 bytes per rune
	l.Record(context.Background(), "p1", long, Entry{Via: ViaConsole, Source: SourceSQL, Outcome: OutcomeOK})
	if w.count() != 1 {
		t.Fatalf("want 1 row, got %d", w.count())
	}
	row := w.rows[0]
	stored := row[argStatement].(string)
	if len(stored) > MaxStatementLen+len("…") || !strings.HasSuffix(stored, "…") {
		t.Errorf("statement not capped: %d bytes", len(stored))
	}
	if !strings.HasPrefix(stored, "é") || strings.ContainsRune(stored[:len(stored)-len("…")], '�') {
		t.Error("cut inside a rune")
	}
	if row[argLen].(int) != len(long) {
		t.Errorf("statement_len = %v, want the full length %d", row[argLen], len(long))
	}
	if len(row[argSHA].(string)) != 64 {
		t.Errorf("sha256 = %q", row[argSHA])
	}
}

func TestRecord_NULIsEscapedButHashedAsSent(t *testing.T) {
	w := &fakeWriter{}
	l := NewWithWriter(w)
	l.Record(context.Background(), "p1", "SELECT 1\x00; DROP TABLE x", Entry{Via: ViaConsole, Source: SourceSQL, Outcome: OutcomeRefused})
	stored := w.rows[0][argStatement].(string)
	if strings.ContainsRune(stored, 0) || !strings.Contains(stored, `\0; DROP TABLE x`) {
		t.Errorf("NUL not escaped: %q", stored)
	}
	if w.rows[0][argLen].(int) != len("SELECT 1\x00; DROP TABLE x") {
		t.Error("length should be of the statement as sent")
	}
}

func TestRecord_NilLoggerAndNoProjectAreNoOps(t *testing.T) {
	var l *Logger
	l.Record(context.Background(), "p1", "SELECT 1", Entry{}) // must not panic
	w := &fakeWriter{}
	NewWithWriter(w).Record(context.Background(), "", "SELECT 1", Entry{})
	if w.count() != 0 {
		t.Error("an entry without a project must not be written")
	}
	if FromContext(context.Background()) != nil {
		t.Error("no logger in an empty context")
	}
}

func TestRecord_WriteBudgetPerProject(t *testing.T) {
	w := &fakeWriter{}
	l := NewWithWriter(w)
	for i := 0; i < logBurst+50; i++ {
		l.Record(context.Background(), "busy", "SELECT 1", Entry{Via: ViaConsole, Source: SourceSQL, Outcome: OutcomeOK})
	}
	if got := w.count(); got > logBurst+5 {
		t.Errorf("budget not applied: %d rows written", got)
	}
	before := w.count()
	l.Record(context.Background(), "quiet", "SELECT 1", Entry{Via: ViaConsole, Source: SourceSQL, Outcome: OutcomeOK})
	if w.count() != before+1 {
		t.Error("one project's budget must not affect another")
	}
}

func TestRefusalAlert_FiresOncePerWindowEvenWhenDropped(t *testing.T) {
	w := &fakeWriter{}
	l := NewWithWriter(w)
	fired := make(chan int, 4)
	l.OnRefusals = func(projectID string, count int) {
		if projectID != "p1" {
			t.Errorf("hook got %q", projectID)
		}
		fired <- count
	}
	// Exhaust the write budget first: refusals must still be counted.
	for i := 0; i < logBurst+10; i++ {
		l.Record(context.Background(), "p1", "SELECT 1", Entry{Via: ViaConsole, Source: SourceSQL, Outcome: OutcomeOK})
	}
	for i := 0; i < RefusalAlertThreshold*3; i++ {
		l.Record(context.Background(), "p1", "DO $$ … $$", Entry{ActorEmail: "a@b.c", Via: ViaConsole, Source: SourceSQL, Outcome: OutcomeRefused})
	}
	select {
	case n := <-fired:
		if n != RefusalAlertThreshold {
			t.Errorf("alert at count %d, want %d", n, RefusalAlertThreshold)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("alert hook not called")
	}
	select {
	case n := <-fired:
		t.Errorf("alert fired twice in one window (count %d)", n)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFromRequest_ViaAndCaller(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.RemoteAddr = "203.0.113.7:51234"
	ctx := audit.WithActor(r.Context(), "11111111-1111-1111-1111-111111111111", "dev@example.com")
	e := FromRequest(r.WithContext(ctx), SourceSQL)
	if e.Via != ViaConsole || e.PATID != "" || e.ActorEmail != "dev@example.com" || e.IP != "203.0.113.7" || e.Source != SourceSQL {
		t.Errorf("console entry = %+v", e)
	}
	// The IP is the trusted hop's view, not a client-chosen header value.
	r2 := httptest.NewRequest("POST", "/x", nil)
	r2.RemoteAddr = "10.0.0.5:4444"
	r2.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.9")
	if ip := FromRequest(r2, SourceSQL).IP; ip != "198.51.100.9" {
		t.Errorf("IP = %q, want the right-most (trusted) hop", ip)
	}
	// A malformed actor id is dropped (stored as NULL), not written.
	ctx = audit.WithActor(r.Context(), "dev-user", "dev@example.com")
	if e := FromRequest(r.WithContext(ctx), SourceSQL); e.ActorID != "" {
		t.Errorf("non-UUID actor id kept: %q", e.ActorID)
	}
	ctx = audit.WithActorToken(ctx, "22222222-2222-2222-2222-222222222222")
	e = FromRequest(r.WithContext(ctx), SourceSQLTransaction)
	if e.Via != ViaToken || e.PATID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("token entry = %+v", e)
	}
}

func TestRecordBatch_OneInsertAndNotRunIsNoRefusal(t *testing.T) {
	w := &fakeWriter{}
	l := NewWithWriter(w)
	fired := 0
	l.OnRefusals = func(string, int) { fired++ }
	items := make([]Item, 40)
	for i := range items {
		items[i] = Item{Statement: "SELECT 1", Entry: Entry{Via: ViaConsole, Source: SourceSQLTransaction, Outcome: OutcomeNotRun}}
	}
	items[3].Entry.Outcome = OutcomeRefused
	l.RecordBatch(context.Background(), "p1", items)
	if w.count() != 1 {
		t.Fatalf("want one INSERT for the batch, got %d", w.count())
	}
	if got := len(w.rows[0]); got != 15*len(items) {
		t.Errorf("want %d args, got %d", 15*len(items), got)
	}
	time.Sleep(50 * time.Millisecond)
	if fired != 0 {
		t.Error("one refused statement plus not-run siblings must not trigger the alert")
	}
}

func TestRecordBatch_PartialBudget(t *testing.T) {
	w := &fakeWriter{}
	l := NewWithWriter(w)
	items := make([]Item, logBurst+20)
	for i := range items {
		items[i] = Item{Statement: "SELECT 1", Entry: Entry{Via: ViaConsole, Source: SourceSQLTransaction, Outcome: OutcomeOK}}
	}
	l.RecordBatch(context.Background(), "p1", items)
	if w.count() != 1 {
		t.Fatalf("want one INSERT, got %d", w.count())
	}
	if n := len(w.rows[0]) / 15; n < logBurst || n > logBurst+5 {
		t.Errorf("wrote %d entries, want about the burst (%d)", n, logBurst)
	}
}
