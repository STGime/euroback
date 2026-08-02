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
	_, err = p.Snapshot(ctx, "id")
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
			"engine": "PostgreSQL-15",
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
	p := newFakeScaleway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/backups") {
			t.Errorf("expected POST .../backups, got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"bkp-1","instance_id":"rdb-1","name":"eurobase-ondemand-abcdef",
			"status":"exporting","size":10485760,"created_at":"2026-08-02T10:00:00Z",
			"expires_at":"2026-08-09T10:00:00Z","same_region":true
		}`))
	})
	snap, err := p.Snapshot(context.Background(), "rdb-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.ProviderID != "bkp-1" || snap.Kind != SnapshotKindOnDemand {
		t.Errorf("unexpected: %+v", snap)
	}
	if snap.SizeMB != 10 {
		t.Errorf("SizeMB: got %d, want 10", snap.SizeMB)
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
