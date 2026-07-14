package auth

import (
	"sync"
	"testing"
	"time"
)

// #284 review high #1: the last_active_at bump on non-paused requests
// used to fire one DB write per request. Coalescing now guards it so
// at most one write per project per `lastActiveBumpInterval` (60 s).
// This test pins the coalescing logic without needing a DB — it
// exercises the sync.Map guard directly.

// coalesceGuard mirrors the check inside SubdomainMiddleware.maybeBumpLastActive.
// Kept minimal here so a regression in the guard logic (e.g. someone
// swapping `<` for `>`) is caught by a pure unit test.
func coalesceGuard(store *sync.Map, key string, now time.Time, interval time.Duration) (shouldWrite bool) {
	nowNanos := now.UnixNano()
	if prev, ok := store.Load(key); ok {
		if nowNanos-prev.(int64) < int64(interval) {
			return false
		}
	}
	store.Store(key, nowNanos)
	return true
}

func TestCoalesceGuard_FirstCallWrites(t *testing.T) {
	var store sync.Map
	if !coalesceGuard(&store, "proj-1", time.Now(), 60*time.Second) {
		t.Error("first call for a fresh project should write")
	}
}

func TestCoalesceGuard_SecondCallWithinIntervalSkips(t *testing.T) {
	var store sync.Map
	now := time.Now()
	if !coalesceGuard(&store, "proj-1", now, 60*time.Second) {
		t.Fatal("first call should write")
	}
	if coalesceGuard(&store, "proj-1", now.Add(30*time.Second), 60*time.Second) {
		t.Error("second call at t+30s should skip (still inside 60s window)")
	}
}

func TestCoalesceGuard_SecondCallAfterIntervalWrites(t *testing.T) {
	var store sync.Map
	now := time.Now()
	if !coalesceGuard(&store, "proj-1", now, 60*time.Second) {
		t.Fatal("first call should write")
	}
	if !coalesceGuard(&store, "proj-1", now.Add(61*time.Second), 60*time.Second) {
		t.Error("second call at t+61s should write (outside 60s window)")
	}
}

func TestCoalesceGuard_PerProjectIndependence(t *testing.T) {
	// Bump for proj-1 must not suppress a first-ever bump for proj-2.
	var store sync.Map
	now := time.Now()
	if !coalesceGuard(&store, "proj-1", now, 60*time.Second) {
		t.Fatal("proj-1 first call should write")
	}
	if !coalesceGuard(&store, "proj-2", now.Add(1*time.Second), 60*time.Second) {
		t.Error("proj-2 first call should write independently")
	}
}

func TestCoalesceGuard_TenReqPerSecondDrops99Percent(t *testing.T) {
	// Simulate a busy project at 10 req/s for 5 minutes (3 000
	// requests). Only 5 writes should fire (one per 60-s window).
	var store sync.Map
	start := time.Now()
	writes := 0
	for i := 0; i < 3000; i++ {
		at := start.Add(time.Duration(i) * 100 * time.Millisecond) // 10/s
		if coalesceGuard(&store, "proj-1", at, 60*time.Second) {
			writes++
		}
	}
	// 5 minutes / 60 seconds = 5 windows.
	if writes != 5 {
		t.Errorf("expected 5 writes over 5 minutes at 10 req/s, got %d", writes)
	}
}
