package dbprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newFakeScaleway wires the Scaleway provider to a caller-provided
// fake server so tests can script the RDB endpoints. Mirrors the
// pattern used by internal/billing/mollie/client_test.go.
func newFakeScaleway(t *testing.T, handler http.HandlerFunc) *Scaleway {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewScaleway(ScalewayConfig{
		SecretKey:     "scw-secret-test",
		ProjectID:     "scw-proj-test",
		DefaultRegion: "fr-par",
		BaseURL:       srv.URL,
	})
}

// TestScaleway_UnauthorizedWhenNoSecret documents the fail-closed
// shape: a client constructed without a secret refuses every method
// without touching the network.
func TestScaleway_UnauthorizedWhenNoSecret(t *testing.T) {
	p := NewScaleway(ScalewayConfig{}) // no secret, no project

	// Every method should return ErrUnauthorized without hitting the
	// (nonexistent) server.
	ctx := context.Background()
	_, err := p.Provision(ctx, ProvisionOpts{ProjectID: "p", Slug: "s"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Provision want ErrUnauthorized, got %v", err)
	}
	_, err = p.Health(ctx, "id")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Health want ErrUnauthorized, got %v", err)
	}
	_, err = p.Snapshot(ctx, "id", SnapshotOpts{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Snapshot want ErrUnauthorized, got %v", err)
	}
	_, err = p.ListSnapshots(ctx, "id")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ListSnapshots want ErrUnauthorized, got %v", err)
	}
	err = p.Delete(ctx, "id")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Delete want ErrUnauthorized, got %v", err)
	}
	// SetBackupSchedule (#457) needs to fail-closed the same way —
	// otherwise a dev environment without SCW_SECRET_KEY would silently
	// accept the call from the reconcile sweeper and log a misleading
	// success. Valid inputs (24h/30d) here so the ErrUnauthorized guard
	// fires before the ErrInvalidRequest floor.
	err = p.SetBackupSchedule(ctx, "id", SetBackupScheduleOpts{FrequencyHours: 24, RetentionDays: 30})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("SetBackupSchedule want ErrUnauthorized, got %v", err)
	}
}

// TestScaleway_ProvisionHappyPath checks the request shape (auth
// header, JSON body) and that the response maps into an Instance.
func TestScaleway_ProvisionHappyPath(t *testing.T) {
	var seenAuth, seenPath string
	var seenBody map[string]any

	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("X-Auth-Token")
		seenPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &seenBody)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id": "rdb-abc-123",
			"name": "eurobase-mysite-aabb",
			"status": "provisioning",
			"engine": "PostgreSQL-16",
			"node_type": "db-gp-s",
			"region": "fr-par",
			"created_at": "2026-08-02T10:00:00Z",
			"endpoints": [
				{"ip": "51.15.1.2", "port": 5432}
			]
		}`))
	})

	got, err := p.Provision(context.Background(), ProvisionOpts{
		ProjectID: "proj-1",
		Slug:      "mysite",
		Size:      SizeMedium,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if seenAuth != "scw-secret-test" {
		t.Errorf("X-Auth-Token: got %q, want scw-secret-test", seenAuth)
	}
	if !strings.HasPrefix(seenPath, "/rdb/v1/regions/fr-par/instances") {
		t.Errorf("path: got %q, want /rdb/v1/regions/fr-par/instances", seenPath)
	}
	if got.ProviderID != "rdb-abc-123" {
		t.Errorf("ProviderID: got %q", got.ProviderID)
	}
	if got.Host != "51.15.1.2" || got.Port != 5432 {
		t.Errorf("endpoint: got %s:%d", got.Host, got.Port)
	}
	if got.State != StateProvisioning {
		t.Errorf("State: got %s, want provisioning", got.State)
	}
	if got.Username != "eurobase_owner" || got.Password == "" {
		t.Errorf("credentials not populated: user=%q, password empty=%v", got.Username, got.Password == "")
	}
	// Body should carry our node_type + tags.
	if seenBody["node_type"] != "db-gp-s" {
		t.Errorf("node_type in body: got %v", seenBody["node_type"])
	}
	if seenBody["project_id"] != "scw-proj-test" {
		t.Errorf("project_id in body: got %v", seenBody["project_id"])
	}
	// Engine in the wire request must match the constant the code
	// uses. Without this, reverting scalewayEngine to an old major
	// leaves the suite green — the changed test-double response
	// alone doesn't bind the wire request to the constant. Regression
	// guard for the PG-15 → PG-16 bump (issue #382).
	if seenBody["engine"] != scalewayEngine {
		t.Errorf("engine in body: got %v, want %s", seenBody["engine"], scalewayEngine)
	}
}

// TestScaleway_ProvisionUnknownSize covers the input-validation
// branch: bad size hint short-circuits before any HTTP call.
func TestScaleway_ProvisionUnknownSize(t *testing.T) {
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call Scaleway on invalid size")
	})
	_, err := p.Provision(context.Background(), ProvisionOpts{
		ProjectID: "p", Slug: "s", Size: Size("xxl-unlisted"),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
}

func TestScaleway_Health(t *testing.T) {
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Health should be GET, got %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"id":"rdb-1","status":"ready","endpoints":[]}`))
	})
	state, err := p.Health(context.Background(), "rdb-1")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if state != StateActive {
		t.Errorf("state: got %s, want active", state)
	}
}

func TestScaleway_StatusMapping(t *testing.T) {
	cases := map[string]State{
		"ready":         StateActive,
		"READY":         StateActive,
		"provisioning":  StateProvisioning,
		"initializing":  StateProvisioning,
		"configuring":   StateProvisioning,
		"snapshotting":  StateProvisioning,
		"restarting":    StateActive,
		"deleting":      StateDeleting,
		"error":         StateFailed,
		"locked":        StateFailed,
		"impossible":    StateUnknown,
		"":              StateUnknown,
	}
	for in, want := range cases {
		if got := mapScalewayStatus(in); got != want {
			t.Errorf("mapScalewayStatus(%q): got %s, want %s", in, got, want)
		}
	}
}

func TestScaleway_Snapshot(t *testing.T) {
	var seenBody map[string]any
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/backups") {
			t.Errorf("expected POST .../backups, got %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &seenBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"bkp-1","instance_id":"rdb-1","name":"eurobase-ondemand-abcdef",
			"status":"exporting","size":10485760,"created_at":"2026-08-02T10:00:00Z",
			"expires_at":"2026-08-09T10:00:00Z","same_region":true
		}`))
	})
	snap, err := p.Snapshot(context.Background(), "rdb-1", SnapshotOpts{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.ProviderID != "bkp-1" || snap.Kind != SnapshotKindOnDemand {
		t.Errorf("unexpected: %+v", snap)
	}
	if snap.SizeMB != 10 {
		t.Errorf("SizeMB: got %d, want 10", snap.SizeMB)
	}
	// merged_bug_001 (retained): request body must carry a non-empty
	// database_name, and — when the caller passes zero Retention —
	// must still omit expires_at so Scaleway doesn't 400 or
	// immediately expire the backup. The zero-Retention path is
	// legal for dev/tests; handlers pass a plan-derived retention.
	if seenBody["database_name"] != "rdb" {
		t.Errorf("database_name in body: got %v, want \"rdb\"", seenBody["database_name"])
	}
	if v, ok := seenBody["expires_at"]; ok {
		t.Errorf("expires_at should be omitted when zero (else Scaleway may 400 or immediately expire), got %v", v)
	}
}

// TestScaleway_SnapshotSendsExpiresAtWhenRetentionSet — closes the
// "backups accumulate indefinitely" hole. When the handler passes a
// non-zero Retention (plan_limits.backup_retention_days for Team),
// the Scaleway request body MUST carry a matching expires_at so the
// provider deletes it on schedule. Regression coverage: without
// this, a code path that silently drops the retention would let
// on-demand backups pile up (150/month per project at the 5/day
// cap × unbounded time × storage cost) before anyone noticed.
func TestScaleway_SnapshotSendsExpiresAtWhenRetentionSet(t *testing.T) {
	var seenBody map[string]any
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &seenBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"bkp-2","instance_id":"rdb-1","name":"eurobase-ondemand-xyz",
			"status":"exporting","size":10485760,"created_at":"2026-08-02T10:00:00Z",
			"expires_at":"2026-09-01T10:00:00Z","same_region":true
		}`))
	})
	before := time.Now()
	_, err := p.Snapshot(context.Background(), "rdb-1", SnapshotOpts{
		Retention: 30 * 24 * time.Hour, // Team plan default
	})
	after := time.Now()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	raw, ok := seenBody["expires_at"].(string)
	if !ok || raw == "" {
		t.Fatalf("expires_at missing from request body, want a timestamp derived from now+Retention. body=%v", seenBody)
	}
	got, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("expires_at not RFC3339: %v (raw=%q)", err, raw)
	}
	// Must fall within [before + 30d, after + 30d] to prove it was
	// derived from the caller's clock at request time, not a
	// stale/wrong value.
	minExp := before.Add(30 * 24 * time.Hour).Add(-time.Second)
	maxExp := after.Add(30 * 24 * time.Hour).Add(time.Second)
	if got.Before(minExp) || got.After(maxExp) {
		t.Errorf("expires_at = %s, want within [%s, %s]", got, minExp, maxExp)
	}
}

// TestScaleway_SetBackupSchedule_HappyPath — verifies the request
// hits POST /rdb/v1/regions/{region}/instances/{id}/set-backup-schedule
// with a body carrying frequency + retention. Regression guard on the
// wire shape (Scaleway's docs use {frequency, retention} for this
// action, distinct from the instance's {backup_schedule_frequency,
// backup_schedule_retention} GET response fields — mixing them up
// would silently 400).
func TestScaleway_SetBackupSchedule_HappyPath(t *testing.T) {
	var seenPath, seenMethod string
	var seenBody map[string]any
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &seenBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"rdb-1","status":"ready","backup_schedule_frequency":24,"backup_schedule_retention":30,"endpoints":[]}`))
	})
	err := p.SetBackupSchedule(context.Background(), "rdb-1", SetBackupScheduleOpts{
		FrequencyHours: 24,
		RetentionDays:  30,
	})
	if err != nil {
		t.Fatalf("SetBackupSchedule: %v", err)
	}
	if seenMethod != http.MethodPost {
		t.Errorf("method: got %s, want POST", seenMethod)
	}
	wantPath := "/rdb/v1/regions/fr-par/instances/rdb-1/set-backup-schedule"
	if seenPath != wantPath {
		t.Errorf("path: got %q, want %q", seenPath, wantPath)
	}
	// JSON numbers unmarshal into float64.
	if seenBody["frequency"].(float64) != 24 {
		t.Errorf("frequency in body: got %v, want 24", seenBody["frequency"])
	}
	if seenBody["retention"].(float64) != 30 {
		t.Errorf("retention in body: got %v, want 30", seenBody["retention"])
	}
}

// TestScaleway_SetBackupSchedule_RejectsZero — the whole feature
// exists to end reliance on Scaleway defaults, so the provider
// method itself refuses zero-value inputs even before hitting the
// network. Handlers + workers ALSO check upstream (double-layer);
// this is the last line of defence.
func TestScaleway_SetBackupSchedule_RejectsZero(t *testing.T) {
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call Scaleway when inputs are zero")
	})
	cases := []SetBackupScheduleOpts{
		{FrequencyHours: 0, RetentionDays: 30},   // zero frequency
		{FrequencyHours: 24, RetentionDays: 0},   // zero retention (the DEFAULT-0 hazard)
		{FrequencyHours: -1, RetentionDays: 30},  // negative frequency
		{FrequencyHours: 24, RetentionDays: -1},  // negative retention
	}
	for _, opts := range cases {
		err := p.SetBackupSchedule(context.Background(), "rdb-1", opts)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("opts=%+v: want ErrInvalidRequest, got %v", opts, err)
		}
	}
}

// TestScaleway_ProvisionPassesIdempotencyKey — bug_002 fix: the
// Idempotency-Key header must land on the create request so that a
// River retry lands on the same billed instance rather than
// spinning up a duplicate.
func TestScaleway_ProvisionPassesIdempotencyKey(t *testing.T) {
	var seenKey string
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		seenKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"rdb-1","status":"provisioning","endpoints":[]}`))
	})
	_, err := p.Provision(context.Background(), ProvisionOpts{
		ProjectID:      "proj-1",
		Slug:           "site",
		Size:           SizeMedium,
		IdempotencyKey: "provision-42",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if seenKey != "provision-42" {
		t.Errorf("Idempotency-Key header: got %q, want provision-42", seenKey)
	}
}

// TestScaleway_ProvisionOmitsIdempotencyKeyWhenEmpty — belt-and-
// suspenders: an empty IdempotencyKey means no header at all
// (rather than sending an empty-string one, which Scaleway may
// reject).
func TestScaleway_ProvisionOmitsIdempotencyKeyWhenEmpty(t *testing.T) {
	var sawHeader bool
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Idempotency-Key"]; ok {
			sawHeader = true
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"rdb-1","status":"provisioning","endpoints":[]}`))
	})
	_, _ = p.Provision(context.Background(), ProvisionOpts{
		ProjectID: "p", Slug: "s", Size: SizeMedium,
	})
	if sawHeader {
		t.Error("empty IdempotencyKey should not send the header at all")
	}
}

func TestScaleway_ListSnapshotsKindClassification(t *testing.T) {
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"total_count":2,
			"backups":[
				{"id":"b1","instance_id":"i","name":"autobackup_2026-08-01","size":0,"created_at":"2026-08-01T00:00:00Z"},
				{"id":"b2","instance_id":"i","name":"eurobase-ondemand-xxx","size":0,"created_at":"2026-08-02T00:00:00Z"}
			]
		}`))
	})
	got, err := p.ListSnapshots(context.Background(), "i")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(got))
	}
	if got[0].Kind != SnapshotKindScheduled {
		t.Errorf("autobackup: kind=%s, want scheduled", got[0].Kind)
	}
	if got[1].Kind != SnapshotKindOnDemand {
		t.Errorf("ondemand: kind=%s, want ondemand", got[1].Kind)
	}
}

func TestScaleway_DeleteIdempotentOn404(t *testing.T) {
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"not_found","message":"already gone"}`))
	})
	if err := p.Delete(context.Background(), "rdb-vanished"); err != nil {
		t.Errorf("Delete on 404 should succeed, got %v", err)
	}
}

func TestScaleway_ClassifyStatus(t *testing.T) {
	cases := map[int]error{
		404: ErrNotFound,
		401: ErrUnauthorized,
		403: ErrUnauthorized,
		429: ErrRateLimited,
		400: ErrInvalidRequest,
		422: ErrInvalidRequest,
		500: ErrProviderUnavailable,
		503: ErrProviderUnavailable,
	}
	for status, want := range cases {
		got := classifyStatus(status, []byte("body"))
		if !errors.Is(got, want) {
			t.Errorf("status %d: got %v, want %v", status, got, want)
		}
	}
}

// TestScaleway_RestoreNameCappedAt63 — bug_011 fix: even for a
// worst-case source name (58 chars, the Provision-time max), the
// composed restored name must fit under Scaleway's 63-char slug
// limit. Uses the actual Restore code path (with a mock backing
// the two API calls Restore makes).
func TestScaleway_RestoreNameCappedAt63(t *testing.T) {
	var seenBody map[string]any
	longSrcName := strings.Repeat("a", 40) + "-" + strings.Repeat("b", 17) // 58 chars, worst-case Provision output
	getCalls := 0
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCalls++
			// source instance lookup
			_, _ = w.Write([]byte(`{
				"id":"rdb-src","name":"` + longSrcName + `","status":"ready",
				"node_type":"db-gp-s","endpoints":[]
			}`))
		case http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &seenBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"rdb-new","status":"provisioning","endpoints":[]}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})
	_, err := p.Restore(context.Background(), "rdb-src", RestoreSource{SnapshotID: "bkp-1"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if getCalls != 1 {
		t.Errorf("expected 1 GET (source lookup), got %d", getCalls)
	}
	name, _ := seenBody["instance_name"].(string)
	if name == "" {
		t.Fatal("instance_name missing from restore request body")
	}
	if len(name) > scalewayMaxInstanceName {
		t.Errorf("restored name over Scaleway's %d-char limit: len=%d, name=%q",
			scalewayMaxInstanceName, len(name), name)
	}
	if strings.HasSuffix(name, "-") {
		t.Errorf("restored name ends in trailing hyphen (invalid slug): %q", name)
	}
}

func TestScaleway_RestoreSourceValidation(t *testing.T) {
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call Scaleway on invalid source")
	})
	ctx := context.Background()
	// Both set → invalid.
	_, err := p.Restore(ctx, "rdb-1", RestoreSource{SnapshotID: "s", PITRTarget: time.Now()})
	if !errors.Is(err, ErrInvalidRestoreSource) {
		t.Errorf("both set: want ErrInvalidRestoreSource, got %v", err)
	}
	// Neither set → invalid.
	_, err = p.Restore(ctx, "rdb-1", RestoreSource{})
	if !errors.Is(err, ErrInvalidRestoreSource) {
		t.Errorf("neither set: want ErrInvalidRestoreSource, got %v", err)
	}
}

func TestScaleway_SanitizeSlug(t *testing.T) {
	cases := map[string]string{
		"hello":         "hello",
		"Hello_World":   "hello-world",
		"MySite-2026!": "mysite-2026",
		"":             "project",
		"$$$":          "project",
		strings.Repeat("a", 100): strings.Repeat("a", 40),
	}
	for in, want := range cases {
		if got := sanitizeSlug(in); got != want {
			t.Errorf("sanitizeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
