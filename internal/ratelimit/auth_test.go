package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func setupAuthTestLimiter(t *testing.T) *RateLimiter {
	t.Helper()
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/1"
	}
	rl, err := NewRateLimiter(redisURL)
	if err != nil {
		t.Skipf("redis not available: %v", err)
	}
	return rl
}

func TestCheckAuthRate_AllowsWithinLimit(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	key := "allow_test_" + time.Now().Format("150405.000")
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		blocked := CheckAuthRate(rl, w, t.Context(), "test_allow", key, 3, 1*time.Minute)
		if blocked {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
}

func TestCheckAuthRate_BlocksWhenExceeded(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	key := "block_test_" + time.Now().Format("150405.000")
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		CheckAuthRate(rl, w, t.Context(), "test_block", key, 3, 1*time.Minute)
	}

	w := httptest.NewRecorder()
	blocked := CheckAuthRate(rl, w, t.Context(), "test_block", key, 3, 1*time.Minute)
	if !blocked {
		t.Fatal("4th request should be blocked")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
}

func TestCheckAuthRate_NilLimiter(t *testing.T) {
	w := httptest.NewRecorder()
	blocked := CheckAuthRate(nil, w, t.Context(), "test_nil", "user@test.com", 1, 1*time.Minute)
	if blocked {
		t.Fatal("nil limiter should allow all requests")
	}
}

func TestSigninFailRate_OnlyCountsFailures(t *testing.T) {
	rl := setupAuthTestLimiter(t)
	defer rl.Close()

	email := "signin_test_" + time.Now().Format("150405.000")

	// Record 5 failures.
	for i := 0; i < 5; i++ {
		RecordSigninFailure(rl, t.Context(), email)
	}

	// Next check should be blocked.
	w := httptest.NewRecorder()
	blocked := CheckSigninFailRate(rl, w, t.Context(), email)
	if !blocked {
		t.Fatal("should be blocked after 5 failures")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
}

func TestClientIP_XForwardedFor(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")
	if ip := ClientIP(r); ip != "1.2.3.4" {
		t.Fatalf("expected 1.2.3.4, got %s", ip)
	}
}

func TestClientIP_RemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "5.6.7.8:12345"
	if ip := ClientIP(r); ip != "5.6.7.8" {
		t.Fatalf("expected 5.6.7.8, got %s", ip)
	}
}
