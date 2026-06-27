package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// #230: at-the-limit + below-the-limit integration tests covering each
// per-project knob end-to-end against a real Redis (skipped when one
// isn't available). Existing unit tests cover the helper contracts
// (project_auth_test.go + quota_test.go + client_ip_test.go); these
// tests verify the *behaviour* the docs page promises: a knob saved to
// the config takes effect on the next request, the window expires, and
// the trust-proxy switch changes the counter identifier so the right
// counter takes the hit.

// helperRequest builds a minimal *http.Request with the given RemoteAddr
// and X-Forwarded-For header — just enough surface for ClientIPForProject
// to make its decision. We reuse this from each table-driven case.
func helperRequest(remoteAddr, xff string) *http.Request {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// TestKnobs_AtTheLimit walks the four per-IP knob shapes (signup_signin,
// token_refresh, token_verify, and a representative per-project hourly
// quota via CheckProjectHourlyQuota) past their cap. For each:
//
//   - N requests pass (below limit)
//   - the (N+1)th returns 429 with Retry-After set
//   - per-project isolation holds: a second project on the same Redis
//     and same identifier still gets through
//
// One table-driven test so a future knob (anonymous sign-ups, Web3,
// whatever lands) is one row away.
func TestKnobs_AtTheLimit(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	cases := []struct {
		name       string
		action     string
		identifier string
		limit      int
		window     time.Duration
	}{
		{"signup_signin per IP per 5min", "signup_signin", "203.0.113.7", 3, 10 * time.Second},
		{"token_refresh per IP per 5min", "token_refresh", "203.0.113.7", 3, 10 * time.Second},
		{"token_verify per IP per 5min", "token_verify", "203.0.113.7", 3, 10 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projA := "projA-" + tc.name + "-" + suffix
			projB := "projB-" + tc.name + "-" + suffix

			// N requests must pass on projA.
			for i := range tc.limit {
				w := httptest.NewRecorder()
				if CheckAuthRateForProject(rl, w, context.Background(), tc.action, projA, tc.identifier, tc.limit, tc.window) {
					t.Fatalf("request %d/%d unexpectedly blocked", i+1, tc.limit)
				}
			}

			// (N+1)th must 429 with Retry-After.
			w := httptest.NewRecorder()
			blocked := CheckAuthRateForProject(rl, w, context.Background(), tc.action, projA, tc.identifier, tc.limit, tc.window)
			if !blocked {
				t.Fatal("over-cap request was allowed")
			}
			if w.Code != http.StatusTooManyRequests {
				t.Errorf("over-cap status: got %d, want 429", w.Code)
			}
			if w.Header().Get("Retry-After") == "" {
				t.Error("over-cap response must set Retry-After header")
			}

			// projB on the same identifier must pass — counter isolation.
			wB := httptest.NewRecorder()
			if CheckAuthRateForProject(rl, wB, context.Background(), tc.action, projB, tc.identifier, tc.limit, tc.window) {
				t.Fatal("projB blocked because projA was capped — counter isolation broken")
			}
		})
	}
}

// Note: the hourly-quota path coverage (CheckProjectHourlyQuota +
// ErrQuotaExceeded for the email/SMS keyspace) lives with PR #234's
// project_quota_test.go — those symbols don't exist on main until #234
// merges, so the at-the-limit + below-the-limit coverage for that
// surface ships alongside the code itself.

// TestKnob_WindowExpiry verifies that an at-cap counter unblocks once the
// limiter's window elapses. We use a tight 1-second window so the test
// stays fast.
func TestKnob_WindowExpiry(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	proj := "proj-expiry-" + suffix
	const limit = 1
	const window = 1100 * time.Millisecond // > 1s so the underlying TTL rounds correctly

	// Hit the cap.
	for range limit {
		w := httptest.NewRecorder()
		if CheckAuthRateForProject(rl, w, context.Background(), "signup_signin", proj, "ip", limit, window) {
			t.Fatalf("first request unexpectedly blocked")
		}
	}
	w := httptest.NewRecorder()
	if !CheckAuthRateForProject(rl, w, context.Background(), "signup_signin", proj, "ip", limit, window) {
		t.Fatal("second request should be blocked")
	}

	// Wait past the window, then verify the budget refilled.
	time.Sleep(window + 200*time.Millisecond)

	w2 := httptest.NewRecorder()
	if CheckAuthRateForProject(rl, w2, context.Background(), "signup_signin", proj, "ip", limit, window) {
		t.Fatal("after window expiry the next request must pass — counter did not reset")
	}
}

// TestKnob_PerProjectOverrideTakesEffect: tightening the limit mid-flight
// (the console save behaviour the docs page promises) is reflected on
// the next call without waiting for the window to expire. We model this
// by hitting one project at limit=N, then a second one at limit=N-1
// from request 1 — the override took effect immediately.
//
// (We can't actually mutate the persisted limit mid-window because the
// limit is a parameter to Allow, not stored state. But the contract the
// caller depends on is: "the next CheckAuthRateForProject reads the
// caller's limit verbatim." That's what we test here.)
func TestKnob_PerProjectOverrideTakesEffect(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	proj := "proj-override-" + suffix
	ip := "ip"

	// At limit=5, 3 requests pass.
	for i := range 3 {
		w := httptest.NewRecorder()
		if CheckAuthRateForProject(rl, w, context.Background(), "signup_signin", proj, ip, 5, 10*time.Second) {
			t.Fatalf("request %d/5 unexpectedly blocked", i+1)
		}
	}

	// Operator tightens the limit to 3 via the console. The very next
	// request (the 4th overall, but the first at limit=3) sees
	// "current count 3 ≥ limit 3" and is blocked.
	w := httptest.NewRecorder()
	if !CheckAuthRateForProject(rl, w, context.Background(), "signup_signin", proj, ip, 3, 10*time.Second) {
		t.Fatal("tightened limit should block the next request — saw allowed")
	}
}

// TestTrustProxy_ChangesCounterIdentifier — when the trust_proxy knob
// flips, the counter that takes the hit changes from "TCP peer" to
// "leftmost XFF" (and vice versa). That has a visible effect: a request
// at the cap under one mode must not block the next request under the
// other (different counter).
func TestTrustProxy_ChangesCounterIdentifier(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	proj := "proj-tp-" + suffix
	const limit = 2
	const window = 10 * time.Second

	// Two requests with TCP peer "10.0.0.5" and XFF "1.2.3.4". With
	// trust_proxy=false, both key on "10.0.0.5" (the TCP peer).
	r := helperRequest("10.0.0.5:54321", "1.2.3.4")
	for i := range limit {
		w := httptest.NewRecorder()
		id := ClientIPForProject(r, false)
		if id != "10.0.0.5" {
			t.Fatalf("trust_proxy=false identifier: got %q, want 10.0.0.5", id)
		}
		if CheckAuthRateForProject(rl, w, context.Background(), "signup_signin", proj, id, limit, window) {
			t.Fatalf("trust_proxy=false: request %d/%d blocked unexpectedly", i+1, limit)
		}
	}
	// Cap reached against the TCP peer.
	w := httptest.NewRecorder()
	if !CheckAuthRateForProject(rl, w, context.Background(), "signup_signin", proj, ClientIPForProject(r, false), limit, window) {
		t.Fatal("trust_proxy=false: over-cap request should be blocked")
	}

	// Now flip the project's trust_proxy. The identifier switches to
	// the leftmost XFF (1.2.3.4) — a different counter — so the next
	// request must pass even though the project + TCP-peer counter is
	// at cap.
	id := ClientIPForProject(r, true)
	if id != "1.2.3.4" {
		t.Fatalf("trust_proxy=true identifier: got %q, want 1.2.3.4", id)
	}
	w2 := httptest.NewRecorder()
	if CheckAuthRateForProject(rl, w2, context.Background(), "signup_signin", proj, id, limit, window) {
		t.Fatal("trust_proxy flip should switch to a fresh counter (different identifier) — saw blocked")
	}
}
