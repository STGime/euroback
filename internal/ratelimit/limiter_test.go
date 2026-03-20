package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"
)

// setupTestLimiter creates a RateLimiter connected to local Redis.
// Skips the test if Redis is unavailable.
func setupTestLimiter(t *testing.T) *RateLimiter {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/1"
	}

	limiter, err := NewRateLimiter(redisURL)
	if err != nil {
		t.Skipf("cannot connect to Redis: %v", err)
	}

	t.Cleanup(func() {
		limiter.Close()
	})

	return limiter
}

func TestAllowWithinLimit(t *testing.T) {
	limiter := setupTestLimiter(t)
	ctx := context.Background()
	key := "test-within-limit-" + t.Name()

	for i := 0; i < 5; i++ {
		allowed, _, err := limiter.Allow(ctx, key, 10, time.Second)
		if err != nil {
			t.Fatalf("Allow call %d returned error: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("Allow call %d should be allowed (within limit 10), but was denied", i+1)
		}
	}
}

func TestAllowExceedsLimit(t *testing.T) {
	limiter := setupTestLimiter(t)
	ctx := context.Background()
	key := "test-exceeds-limit-" + t.Name()

	limit := 10
	// Make 10 allowed calls.
	for i := 0; i < limit; i++ {
		allowed, _, err := limiter.Allow(ctx, key, limit, time.Second)
		if err != nil {
			t.Fatalf("Allow call %d returned error: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("Allow call %d should be allowed, but was denied", i+1)
		}
	}

	// The 11th call should be denied.
	allowed, _, err := limiter.Allow(ctx, key, limit, time.Second)
	if err != nil {
		t.Fatalf("Allow call 11 returned error: %v", err)
	}
	if allowed {
		t.Error("11th call should be denied (exceeds limit of 10), but was allowed")
	}
}

func TestRateLimitInfo(t *testing.T) {
	limiter := setupTestLimiter(t)
	ctx := context.Background()
	key := "test-info-" + t.Name()

	limit := 10

	// First call: remaining should be limit - 1.
	_, info1, err := limiter.Allow(ctx, key, limit, 5*time.Second)
	if err != nil {
		t.Fatalf("Allow call 1 returned error: %v", err)
	}
	if info1.Limit != int64(limit) {
		t.Errorf("expected Limit=%d, got %d", limit, info1.Limit)
	}
	if info1.Remaining != int64(limit-1) {
		t.Errorf("expected Remaining=%d after 1st call, got %d", limit-1, info1.Remaining)
	}

	// Second call: remaining should decrease.
	_, info2, err := limiter.Allow(ctx, key, limit, 5*time.Second)
	if err != nil {
		t.Fatalf("Allow call 2 returned error: %v", err)
	}
	if info2.Remaining != int64(limit-2) {
		t.Errorf("expected Remaining=%d after 2nd call, got %d", limit-2, info2.Remaining)
	}
	if info2.Remaining >= info1.Remaining {
		t.Error("expected Remaining to decrease between calls")
	}

	// ResetAt should be a future timestamp.
	if info2.ResetAt <= time.Now().Unix()-1 {
		t.Error("expected ResetAt to be in the future")
	}
}

func TestDifferentKeys(t *testing.T) {
	limiter := setupTestLimiter(t)
	ctx := context.Background()
	keyA := "test-key-a-" + t.Name()
	keyB := "test-key-b-" + t.Name()

	limit := 5

	// Exhaust key A.
	for i := 0; i < limit; i++ {
		_, _, err := limiter.Allow(ctx, keyA, limit, time.Second)
		if err != nil {
			t.Fatalf("Allow keyA call %d returned error: %v", i+1, err)
		}
	}

	// Key A should now be denied.
	allowedA, _, err := limiter.Allow(ctx, keyA, limit, time.Second)
	if err != nil {
		t.Fatalf("Allow keyA overflow returned error: %v", err)
	}
	if allowedA {
		t.Error("keyA should be denied after exhausting limit")
	}

	// Key B should still be allowed.
	allowedB, _, err := limiter.Allow(ctx, keyB, limit, time.Second)
	if err != nil {
		t.Fatalf("Allow keyB returned error: %v", err)
	}
	if !allowedB {
		t.Error("keyB should still be allowed (different key from keyA)")
	}
}
