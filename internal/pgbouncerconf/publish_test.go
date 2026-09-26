package pgbouncerconf

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingStore struct{ Store }

func (failingStore) Put(context.Context, map[string][]byte) error { return errors.New("api down") }

func TestPublisher(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := &Publisher{Store: DirStore(dir), Secret: []byte(strings.Repeat("s", 32)), Upstream: "h:5432/db", Refresh: time.Hour}

	// First pass writes even with zero tenants (pooler init waits for it).
	if wrote, err := p.PublishSchemas(ctx, nil); err != nil || !wrote {
		t.Fatalf("first publish: wrote=%v err=%v", wrote, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, KeyUpstream)); string(b) != "h:5432/db" {
		t.Errorf("upstream = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, KeyPublishedAt)); len(b) == 0 {
		t.Error("published_at missing")
	}

	// Unchanged within Refresh: no write.
	if wrote, _ := p.PublishSchemas(ctx, nil); wrote {
		t.Error("rewrote an unchanged list within Refresh")
	}
	// Changed: write.
	if wrote, _ := p.PublishSchemas(ctx, []string{"tenant_a"}); !wrote {
		t.Error("did not publish a changed list")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, KeyUserlist)); !strings.Contains(string(b), `"tenant_a_func"`) {
		t.Errorf("userlist = %q", b)
	}
	// Refresh elapsed: rewrite even unchanged.
	p.Refresh = time.Nanosecond
	time.Sleep(time.Millisecond)
	if wrote, _ := p.PublishSchemas(ctx, []string{"tenant_a"}); !wrote {
		t.Error("did not rewrite after Refresh")
	}

	// A failed write is retried on the next call.
	q := &Publisher{Store: failingStore{DirStore(dir)}, Secret: p.Secret, Upstream: p.Upstream, Refresh: time.Hour}
	if _, err := q.PublishSchemas(ctx, nil); err == nil {
		t.Fatal("want error from failing store")
	}
	q.Store = DirStore(dir)
	if wrote, err := q.PublishSchemas(ctx, nil); err != nil || !wrote {
		t.Errorf("retry after failure: wrote=%v err=%v", wrote, err)
	}
}
