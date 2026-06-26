package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// #225: per-project rate limit helper. Covers two contracts that the
// rest of the series relies on:
//
//   1. The Redis key shape `auth:{action}:project:{projectID}:{ip}` is
//      project-namespaced — two tenants colliding on identifier (a
//      shared NAT'd office IP) get independent counters. Tested by
//      hammering one project to its cap and confirming the other still
//      gets through.
//   2. Fail-open when the limiter is nil (Redis not configured —
//      typical in local dev). Tested without a Redis dependency so
//      this test runs in unit-only CI.

func TestCheckAuthRateForProject_NilLimiterFailsOpen(t *testing.T) {
	w := httptest.NewRecorder()
	blocked := CheckAuthRateForProject(nil, w, context.Background(), "signup_signin", "proj-1", "1.2.3.4", 1, time.Minute)
	if blocked {
		t.Fatal("nil limiter must fail-open (allow every request)")
	}
	if w.Code != http.StatusOK {
		t.Errorf("nil limiter wrote a non-200 status: %d", w.Code)
	}
}

func TestCheckAuthRateForProject_IsolatesTenants(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	const limit = 2
	const win = 10 * time.Second
	// Use unique tenant IDs per run so prior test runs don't poison
	// the counter (we share a Redis instance with other tests).
	suffix := time.Now().Format("150405.000")
	projA := "projA-" + suffix
	projB := "projB-" + suffix
	ip := "10.0.0.1"

	// Hit projA up to its cap.
	for i := 0; i < limit; i++ {
		w := httptest.NewRecorder()
		if CheckAuthRateForProject(rl, w, context.Background(), "signup_signin", projA, ip, limit, win) {
			t.Fatalf("projA request %d/%d blocked unexpectedly", i+1, limit)
		}
	}
	// The next projA request must block.
	w := httptest.NewRecorder()
	if !CheckAuthRateForProject(rl, w, context.Background(), "signup_signin", projA, ip, limit, win) {
		t.Fatal("projA over-cap should be blocked, was allowed")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("blocked response status: got %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("blocked response must set Retry-After")
	}

	// projB with the SAME identifier (same IP) must still pass — the
	// counters are isolated by project_id. This is the load-bearing
	// invariant of the per-project shape.
	wB := httptest.NewRecorder()
	if CheckAuthRateForProject(rl, wB, context.Background(), "signup_signin", projB, ip, limit, win) {
		t.Fatal("projB request blocked even though only projA hit its cap — counter isolation broken")
	}
}

// FiveMinutes is a named constant the handlers depend on — snapshot it
// to catch an accidental edit.
func TestFiveMinutes_Constant(t *testing.T) {
	if FiveMinutes != 5*time.Minute {
		t.Fatalf("FiveMinutes drifted: got %v, want 5m", FiveMinutes)
	}
}
