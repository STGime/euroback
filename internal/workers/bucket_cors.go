package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// bucketCORSSetter is the part of storage.S3Client the CORS pass uses.
type bucketCORSSetter interface {
	EnsureUploadCORS(ctx context.Context, bucketName string) error
}

// StartBucketCORS sets the browser CORS rule (S3Client.EnsureUploadCORS)
// on every active project's bucket at startup and then daily, so signed
// upload URLs work from browsers on buckets created before the rule
// existed, and a bucket whose rule was lost is repaired.
func StartBucketCORS(ctx context.Context, pool *pgxpool.Pool, s3 bucketCORSSetter) {
	if s3 == nil {
		return
	}
	go func() {
		syncBucketCORS(ctx, pool, s3)
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				syncBucketCORS(ctx, pool, s3)
			}
		}
	}()
}

func syncBucketCORS(ctx context.Context, pool *pgxpool.Pool, s3 bucketCORSSetter) {
	rows, err := pool.Query(ctx,
		`SELECT DISTINCT s3_bucket FROM public.projects
		  WHERE status = 'active' AND coalesce(s3_bucket, '') <> ''`)
	if err != nil {
		slog.Error("bucket cors: list buckets failed", "error", err)
		return
	}
	var buckets []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err == nil {
			buckets = append(buckets, b)
		}
	}
	rows.Close()
	failed := 0
	for _, b := range buckets {
		if ctx.Err() != nil {
			return
		}
		bctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := s3.EnsureUploadCORS(bctx, b); err != nil {
			failed++
			slog.Warn("bucket cors: set failed", "bucket", b, "error", err)
		}
		cancel()
	}
	slog.Info("bucket cors: pass done", "buckets", len(buckets), "failed", failed)
}
