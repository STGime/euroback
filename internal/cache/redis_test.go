package cache

import (
	"context"
	"os"
	"testing"
	"time"
)

// setupTestRedis creates a RedisCache pointing to the local Redis instance.
func setupTestRedis(t *testing.T) *RedisCache {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6380"
	}

	cache, err := NewRedisCache(redisURL)
	if err != nil {
		t.Skipf("cannot connect to Redis (not running?): %v", err)
	}

	t.Cleanup(func() {
		cache.Close()
	})

	return cache
}

func TestSetGet(t *testing.T) {
	cache := setupTestRedis(t)
	ctx := context.Background()

	key := "test:setget:" + t.Name()
	value := "hello-eurobase"

	t.Cleanup(func() {
		_ = cache.Delete(ctx, key)
	})

	err := cache.Set(ctx, key, value, 10*time.Second)
	if err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}

	got, err := cache.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}
	if got != value {
		t.Errorf("expected %q, got %q", value, got)
	}
}

func TestDelete(t *testing.T) {
	cache := setupTestRedis(t)
	ctx := context.Background()

	key := "test:delete:" + t.Name()

	err := cache.Set(ctx, key, "to-be-deleted", 10*time.Second)
	if err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}

	err = cache.Delete(ctx, key)
	if err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	_, err = cache.Get(ctx, key)
	if err == nil {
		t.Error("expected error after deleting key, got nil")
	}
}

func TestTenantCache(t *testing.T) {
	cache := setupTestRedis(t)
	ctx := context.Background()

	tenantID := "test-tenant-cache-id"

	t.Cleanup(func() {
		_ = cache.Delete(ctx, "tenant:"+tenantID)
	})

	info := &TenantInfo{
		ProjectID:  "proj-123",
		SchemaName: "tenant_proj_123",
		S3Bucket:   "eurobase-proj-123",
		Plan:       "free",
		Status:     "active",
	}

	err := cache.SetTenant(ctx, tenantID, info, 10*time.Second)
	if err != nil {
		t.Fatalf("SetTenant() returned error: %v", err)
	}

	got, err := cache.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant() returned error: %v", err)
	}
	if got == nil {
		t.Fatal("GetTenant() returned nil")
	}

	if got.ProjectID != info.ProjectID {
		t.Errorf("ProjectID: expected %q, got %q", info.ProjectID, got.ProjectID)
	}
	if got.SchemaName != info.SchemaName {
		t.Errorf("SchemaName: expected %q, got %q", info.SchemaName, got.SchemaName)
	}
	if got.S3Bucket != info.S3Bucket {
		t.Errorf("S3Bucket: expected %q, got %q", info.S3Bucket, got.S3Bucket)
	}
	if got.Plan != info.Plan {
		t.Errorf("Plan: expected %q, got %q", info.Plan, got.Plan)
	}
	if got.Status != info.Status {
		t.Errorf("Status: expected %q, got %q", info.Status, got.Status)
	}
}
