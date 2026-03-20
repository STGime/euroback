// Package ratelimit provides Redis-backed rate limiting for the Eurobase gateway.
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// PlanLimits maps billing plan names to their per-second request limits.
var PlanLimits = map[string]int{
	"free": 100,
	"pro":  1000,
}

// RateLimitInfo contains rate limit state returned alongside allow/deny decisions.
type RateLimitInfo struct {
	Limit     int64
	Remaining int64
	ResetAt   int64 // Unix timestamp (seconds) when the window resets
}

// RateLimiter implements sliding-window rate limiting backed by Redis.
type RateLimiter struct {
	client *redis.Client
}

// luaScript atomically increments a counter and sets expiry in one round-trip.
// KEYS[1] = rate limit key
// ARGV[1] = limit (max requests)
// ARGV[2] = window in seconds
// Returns: {current_count, ttl_remaining}
var luaScript = redis.NewScript(`
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])

local current = redis.call("INCR", key)
if current == 1 then
    redis.call("EXPIRE", key, window)
end

local ttl = redis.call("TTL", key)
if ttl == -1 then
    redis.call("EXPIRE", key, window)
    ttl = window
end

return {current, ttl}
`)

// NewRateLimiter creates a new RateLimiter connected to the given Redis URL.
func NewRateLimiter(redisURL string) (*RateLimiter, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	slog.Info("rate limiter redis connected", "addr", opts.Addr)

	return &RateLimiter{client: client}, nil
}

// Allow checks whether the request identified by key is within its rate limit.
// limit is the maximum number of requests allowed within the given window.
func (rl *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, RateLimitInfo, error) {
	redisKey := "ratelimit:" + key
	windowSec := int(window.Seconds())
	if windowSec < 1 {
		windowSec = 1
	}

	result, err := luaScript.Run(ctx, rl.client, []string{redisKey}, limit, windowSec).Int64Slice()
	if err != nil {
		return false, RateLimitInfo{}, fmt.Errorf("run rate limit script: %w", err)
	}

	current := result[0]
	ttl := result[1]
	resetAt := time.Now().Add(time.Duration(ttl) * time.Second).Unix()

	remaining := int64(limit) - current
	if remaining < 0 {
		remaining = 0
	}

	info := RateLimitInfo{
		Limit:     int64(limit),
		Remaining: remaining,
		ResetAt:   resetAt,
	}

	allowed := current <= int64(limit)
	return allowed, info, nil
}

// Close closes the underlying Redis client.
func (rl *RateLimiter) Close() error {
	return rl.client.Close()
}
