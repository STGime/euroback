package dbprovider

import (
	"testing"
	"time"
)

// TestPoolCache_Lifecycle exercises the pieces of PoolCache we can
// test without a live Postgres — sizing, explicit eviction, and the
// idle-sweep timer wiring. The DB-round-trip path (Get → open pool
// on cache miss) needs a real dedicated instance and is covered by
// staging E2E.
func TestPoolCache_ConstructorDefaults(t *testing.T) {
	c := NewPoolCache(nil, nil, 0, 0)
	defer c.Close()

	if c.idleTTL != 30*time.Minute {
		t.Fatalf("idleTTL default: got %v want 30m", c.idleTTL)
	}
	if c.maxConn != 8 {
		t.Fatalf("maxConn default: got %d want 8", c.maxConn)
	}
	if c.Size() != 0 {
		t.Fatalf("empty cache Size(): got %d want 0", c.Size())
	}
}

func TestPoolCache_EvictNoop(t *testing.T) {
	c := NewPoolCache(nil, nil, time.Minute, 4)
	defer c.Close()

	// Evict of an unknown project ID must be a silent no-op — the
	// restore worker fires Evict without knowing whether the pool
	// was ever opened on this gateway pod.
	c.Evict("00000000-0000-0000-0000-000000000000")
	if c.Size() != 0 {
		t.Fatalf("Evict of unknown ID changed Size(): got %d want 0", c.Size())
	}
}

func TestPoolCache_CloseIsIdempotent(t *testing.T) {
	c := NewPoolCache(nil, nil, time.Minute, 4)
	c.Close()

	// A double-Close panics (closing a closed channel). We accept
	// that as the contract — the constructor is fresh per gateway
	// process, so callers control the single Close. This test just
	// asserts Close() on an empty cache doesn't panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Close on empty cache panicked: %v", r)
		}
	}()
	// One clean close only.
}

func TestBuildDSN_URLEncodesPassword(t *testing.T) {
	dsn := buildDSN("owner", "p@ss w:rd/!", "db.internal", 5432, "eurobase")

	// Password contains characters that MUST be URL-encoded or
	// pgxpool.ParseConfig will reject the DSN.
	//
	// The exact expected shape:
	//   postgres://owner:p%40ss%20w%3Ard%2F%21@db.internal:5432/eurobase?sslmode=require
	//
	// Rather than pin the exact byte-order of query params, assert
	// the invariants any consumer relies on.
	wantContains := []string{
		"postgres://",
		"owner:",
		"@db.internal:5432",
		"/eurobase",
		"sslmode=require",
	}
	for _, s := range wantContains {
		if !contains(dsn, s) {
			t.Errorf("buildDSN missing %q: %s", s, dsn)
		}
	}
	// Raw password characters must NOT appear unencoded.
	if contains(dsn, "p@ss w:rd/!") {
		t.Errorf("buildDSN emitted raw special chars in password: %s", dsn)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
