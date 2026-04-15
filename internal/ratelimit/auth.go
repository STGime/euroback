package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Auth endpoint rate limit configuration.
const (
	SignupLimit          = 5
	SignupWindow         = 1 * time.Hour
	SigninFailLimit      = 5
	SigninFailWindow     = 15 * time.Minute
	ForgotPasswordLimit  = 3
	ForgotPasswordWindow = 15 * time.Minute
	ResendVerifyLimit    = 1
	ResendVerifyWindow   = 5 * time.Minute
	MagicLinkLimit       = 3
	MagicLinkWindow      = 15 * time.Minute
	PhoneOTPLimit        = 3
	PhoneOTPWindow       = 15 * time.Minute
)

// CheckAuthRate checks the rate limit for an auth action and writes a 429
// response if exceeded. Returns true if the request should be blocked.
// If the limiter is nil (Redis not configured), always allows.
func CheckAuthRate(limiter *RateLimiter, w http.ResponseWriter, ctx context.Context, action, identifier string, limit int, window time.Duration) bool {
	if limiter == nil {
		return false
	}

	key := fmt.Sprintf("auth:%s:%s", action, identifier)
	allowed, info, _ := limiter.Allow(ctx, key, limit, window)
	if !allowed {
		resetTime := time.Unix(info.ResetAt, 0)
		retryAfter := time.Until(resetTime).Seconds()
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter))
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", info.Limit))
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", info.ResetAt))
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"error":"too many requests, try again in %.0f seconds"}`, retryAfter)
		return true
	}
	return false
}

// RecordSigninFailure increments the signin failure counter for an email
// without checking the limit. Called after a failed signin attempt so that
// successful logins don't consume the budget.
func RecordSigninFailure(limiter *RateLimiter, ctx context.Context, email string) {
	if limiter == nil {
		return
	}
	key := fmt.Sprintf("auth:signin_fail:%s", email)
	_, _, _ = limiter.Allow(ctx, key, SigninFailLimit, SigninFailWindow)
}

// CheckSigninFailRate checks whether the signin failure limit has been
// exceeded for the given email. Returns true if blocked.
func CheckSigninFailRate(limiter *RateLimiter, w http.ResponseWriter, ctx context.Context, email string) bool {
	return CheckAuthRate(limiter, w, ctx, "signin_fail", email, SigninFailLimit, SigninFailWindow)
}

// ClientIP extracts the client IP from a request, preferring X-Forwarded-For.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First IP in the chain is the client.
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	// Strip port from RemoteAddr.
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
