package ratelimit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/auth"
)

// RateLimitMiddleware returns chi-compatible middleware that enforces per-tenant
// rate limits. If the limiter is nil, all requests are allowed through (graceful
// degradation when Redis is unavailable).
func RateLimitMiddleware(limiter *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Graceful degradation: if limiter is nil, skip rate limiting.
			if limiter == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Determine the rate limit key: prefer authenticated tenant ID,
			// fall back to client IP.
			key := r.RemoteAddr
			if claims, ok := auth.ClaimsFromContext(r.Context()); ok && claims.Subject != "" {
				key = claims.Subject
			}

			// Determine rate limit from project plan.
			limit := PlanLimits["free"]
			if pc, ok := auth.ProjectFromContext(r.Context()); ok && pc.Plan != "" {
				if planLimit, exists := PlanLimits[pc.Plan]; exists {
					limit = planLimit
				}
			}
			window := time.Second

			allowed, info, err := limiter.Allow(r.Context(), key, limit, window)
			if err != nil {
				// Redis error: allow request through but log the failure.
				slog.Warn("rate limiter error, allowing request",
					"error", err,
					"key", key,
				)
				next.ServeHTTP(w, r)
				return
			}

			// Always set rate limit headers.
			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", info.Limit))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", info.Remaining))
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", info.ResetAt))

			if !allowed {
				retryAfter := info.ResetAt - time.Now().Unix()
				if retryAfter < 1 {
					retryAfter = 1
				}

				slog.Warn("rate limit exceeded",
					"key", key,
					"limit", info.Limit,
					"reset_at", info.ResetAt,
				)

				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]string{
					"error":   "rate_limit_exceeded",
					"message": "Too many requests. Please retry after the Retry-After period.",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
