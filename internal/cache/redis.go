// Package cache provides a Redis-backed caching layer for Eurobase.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// TenantInfo holds cached tenant metadata.
type TenantInfo struct {
	ProjectID  string `json:"project_id"`
	SchemaName string `json:"schema_name"`
	S3Bucket   string `json:"s3_bucket"`
	Plan       string `json:"plan"`
	Status     string `json:"status"`
}

// RedisCache wraps a Redis client for key-value caching.
type RedisCache struct {
	client *redis.Client
}

// NewRedisCache parses the given Redis URL, creates a client, and verifies
// connectivity with a PING command.
func NewRedisCache(redisURL string) (*RedisCache, error) {
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

	slog.Info("redis cache connected", "addr", opts.Addr)

	return &RedisCache{client: client}, nil
}

// Get retrieves a string value by key. Returns redis.Nil error if key does not exist.
func (c *RedisCache) Get(ctx context.Context, key string) (string, error) {
	return c.client.Get(ctx, key).Result()
}

// Set stores a string value with the given TTL.
func (c *RedisCache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

// Delete removes a key from the cache.
func (c *RedisCache) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

// GetTenant retrieves cached tenant info by tenant ID.
// Returns nil and no error if the key does not exist.
func (c *RedisCache) GetTenant(ctx context.Context, tenantID string) (*TenantInfo, error) {
	key := "tenant:" + tenantID
	data, err := c.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get tenant cache: %w", err)
	}

	var info TenantInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, fmt.Errorf("unmarshal tenant info: %w", err)
	}

	return &info, nil
}

// SetTenant caches tenant info with the given TTL.
func (c *RedisCache) SetTenant(ctx context.Context, tenantID string, info *TenantInfo, ttl time.Duration) error {
	key := "tenant:" + tenantID
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal tenant info: %w", err)
	}

	return c.client.Set(ctx, key, string(data), ttl).Err()
}

// Close closes the underlying Redis client connection.
func (c *RedisCache) Close() error {
	return c.client.Close()
}
