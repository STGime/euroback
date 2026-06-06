package audit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ── Pure unit tests (no DB; run under -short) ──

func TestEffectiveRole(t *testing.T) {
	cases := []struct {
		keyType, endUserID, want string
	}{
		{"secret", "", roleService},
		{"public", "", roleAnon},
		{"", "", roleAnon},
		{"secret", "user-1", roleAuthenticated}, // end-user identity wins over key type
		{"public", "user-1", roleAuthenticated},
	}
	for _, c := range cases {
		if got := EffectiveRole(c.keyType, c.endUserID); got != c.want {
			t.Errorf("EffectiveRole(%q,%q) = %q, want %q", c.keyType, c.endUserID, got, c.want)
		}
	}
}

func TestRecord_NilAndDisabledAreNoOps(t *testing.T) {
	// A nil recorder must be safe — callers don't nil-check.
	var nilRec *AccessRecorder
	nilRec.Record(AccessEvent{Action: AccessActionRead})
	nilRec.Close(context.Background())

	// A disabled recorder must also no-op without a channel.
	disabled := &AccessRecorder{cfg: AccessRecorderConfig{Enabled: false}}
	disabled.Record(AccessEvent{Action: AccessActionRead})
	disabled.Close(context.Background())
}

// newTestRecorder builds an enabled recorder with a buffered channel but NO
// background worker, so enqueued events stay observable in the channel.
func newTestRecorder(buf int, readSample float64) *AccessRecorder {
	return &AccessRecorder{
		cfg: AccessRecorderConfig{Enabled: true, ReadSample: readSample},
		ch:  make(chan AccessEvent, buf),
	}
}

func TestRecord_SamplingDropsReadsButNotExports(t *testing.T) {
	r := newTestRecorder(8, 0.0) // sample 0 → never log reads

	r.Record(AccessEvent{Action: AccessActionRead})
	if len(r.ch) != 0 {
		t.Errorf("read enqueued despite ReadSample=0: len=%d", len(r.ch))
	}

	// Exports and downloads must bypass sampling entirely.
	r.Record(AccessEvent{Action: AccessActionExport})
	r.Record(AccessEvent{Action: AccessActionDownload})
	if len(r.ch) != 2 {
		t.Errorf("export/download were sampled out: len=%d, want 2", len(r.ch))
	}
}

func TestRecord_SampleAllKeepsReads(t *testing.T) {
	r := newTestRecorder(8, 1.0)
	r.Record(AccessEvent{Action: AccessActionRead})
	if len(r.ch) != 1 {
		t.Errorf("read dropped despite ReadSample=1: len=%d, want 1", len(r.ch))
	}
}

func TestRecord_DropsWhenBufferFull(t *testing.T) {
	r := newTestRecorder(1, 1.0)
	r.Record(AccessEvent{Action: AccessActionRead}) // fills the buffer
	r.Record(AccessEvent{Action: AccessActionRead}) // must be dropped, not block

	if len(r.ch) != 1 {
		t.Errorf("buffer len = %d, want 1", len(r.ch))
	}
	if r.dropped != 1 {
		t.Errorf("dropped = %d, want 1", r.dropped)
	}
}

func TestNewAccessRecorder_DisabledWhenConfigured(t *testing.T) {
	r := NewAccessRecorder(nil, AccessRecorderConfig{Enabled: false})
	if r.cfg.Enabled {
		t.Error("recorder should be disabled")
	}
	// No worker, no panic.
	r.Record(AccessEvent{Action: AccessActionRead})
	r.Close(context.Background())
}

// ── Integration test (needs a DB; skipped under -short) ──

func setupAccessTest(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}

	hankoUserID := fmt.Sprintf("test-access-%d", os.Getpid())
	var ownerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (hanko_user_id, email)
		 VALUES ($1, $2)
		 ON CONFLICT (hanko_user_id) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		hankoUserID, "accesstest@eurobase.app",
	).Scan(&ownerID); err != nil {
		pool.Close()
		t.Skipf("cannot create test platform user: %v", err)
	}

	slug := fmt.Sprintf("test-access-%d", os.Getpid())
	var projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'provisioning')
		 RETURNING id`,
		ownerID, "Access Test", slug, "tenant_test_access", "eurobase-test-access", "fr-par", "free",
	).Scan(&projectID); err != nil {
		pool.Close()
		t.Skipf("cannot create test project: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM public.data_access_log WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
		pool.Close()
	})

	return pool, projectID
}

// TestAccessRecorder_PersistsBatchedEvents records a few events through the
// async recorder, drains it, and confirms they landed in data_access_log with
// the expected shape — the core behaviour the GDPR access log relies on.
func TestAccessRecorder_PersistsBatchedEvents(t *testing.T) {
	pool, projectID := setupAccessTest(t)
	ctx := context.Background()

	rec := NewAccessRecorder(pool, AccessRecorderConfig{
		Enabled:       true,
		ReadSample:    1.0,
		BufferSize:    16,
		BatchSize:     2,
		FlushInterval: 50 * time.Millisecond,
	})

	rec.Record(AccessEvent{
		ProjectID: projectID, EndUserID: "", ActorRole: roleService,
		Action: AccessActionRead, TargetTable: "customers",
		TargetKeys: map[string]interface{}{"id": "42"}, IP: "203.0.113.7",
	})
	rec.Record(AccessEvent{
		ProjectID: projectID, ActorRole: roleAuthenticated,
		Action: AccessActionExport, TargetTable: "*",
		TargetKeys: map[string]interface{}{"scope": "user"},
	})

	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rec.Close(drainCtx)

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM public.data_access_log WHERE project_id = $1`, projectID,
	).Scan(&count); err != nil {
		t.Fatalf("count data_access_log: %v", err)
	}
	if count != 2 {
		t.Fatalf("rows persisted = %d, want 2", count)
	}

	var role, action, table, ip string
	var keys string
	if err := pool.QueryRow(ctx,
		`SELECT actor_role, action, target_table, COALESCE(ip,''), target_keys::text
		   FROM public.data_access_log
		  WHERE project_id = $1 AND action = $2`, projectID, AccessActionRead,
	).Scan(&role, &action, &table, &ip, &keys); err != nil {
		t.Fatalf("read back read-event: %v", err)
	}
	if role != roleService || table != "customers" || ip != "203.0.113.7" {
		t.Errorf("read event mismatch: role=%q table=%q ip=%q", role, table, ip)
	}
	if keys != `{"id": "42"}` {
		t.Errorf("target_keys = %q, want {\"id\": \"42\"}", keys)
	}
}
