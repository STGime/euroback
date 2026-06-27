package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

// #227: per-project hourly quota helper. Two contracts the email/SMS
// send paths depend on:
//
//   1. Nil limiter → fail-open. The email path inside AuthService
//      checks the err and skips the send only when ErrQuotaExceeded;
//      anything else (including nil) lets the send proceed. A Redis
//      outage must not silently disable every project's auth flow.
//   2. Per-project isolation. Two tenants on a shared Redis must
//      not share a quota counter. The key is namespaced by
//      project_id, same shape as CheckAuthRateForProject. Tested by
//      hammering one project to its cap and verifying the other
//      still gets through.

func TestCheckProjectHourlyQuota_NilLimiterFailsOpen(t *testing.T) {
	retry, err := CheckProjectHourlyQuota(nil, context.Background(), "email", "proj-1", 1)
	if err != nil {
		t.Fatalf("nil limiter must fail open, got err=%v", err)
	}
	if retry != 0 {
		t.Errorf("nil limiter retryAfter should be 0, got %d", retry)
	}
}

func TestCheckProjectHourlyQuota_IsolatesProjects(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	const cap = 2
	// Unique IDs per run so prior runs don't pollute the counter.
	suffix := time.Now().Format("150405.000")
	projA := "projA-" + suffix
	projB := "projB-" + suffix

	// Hit projA up to cap (cap successes), then assert blocked.
	for i := 0; i < cap; i++ {
		if _, err := CheckProjectHourlyQuota(rl, context.Background(), "email", projA, cap); err != nil {
			t.Fatalf("projA send %d/%d unexpectedly blocked: %v", i+1, cap, err)
		}
	}
	retry, err := CheckProjectHourlyQuota(rl, context.Background(), "email", projA, cap)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("projA over-cap should return ErrQuotaExceeded, got %v", err)
	}
	if retry < 1 {
		t.Errorf("over-cap retryAfter should be >= 1s, got %d", retry)
	}

	// projB must still pass — separate project, separate bucket.
	if _, err := CheckProjectHourlyQuota(rl, context.Background(), "email", projB, cap); err != nil {
		t.Fatalf("projB blocked even though only projA hit its cap — isolation broken: %v", err)
	}
}

// ActionsDontShareKey: a project's email cap and SMS cap are independent
// even though they share the helper. A project at its email cap must not
// be blocked from sending SMS, and vice versa.
func TestCheckProjectHourlyQuota_ActionsAreIndependent(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	suffix := time.Now().Format("150405.000")
	proj := "proj-" + suffix
	const cap = 1

	// Fill the email bucket.
	if _, err := CheckProjectHourlyQuota(rl, context.Background(), "email", proj, cap); err != nil {
		t.Fatalf("email send 1/1 unexpectedly blocked: %v", err)
	}
	if _, err := CheckProjectHourlyQuota(rl, context.Background(), "email", proj, cap); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("email over-cap should return ErrQuotaExceeded, got %v", err)
	}

	// SMS should still pass even though email is at cap.
	if _, err := CheckProjectHourlyQuota(rl, context.Background(), "sms", proj, cap); err != nil {
		t.Fatalf("SMS blocked because email was at cap — action keyspace not independent: %v", err)
	}
}
