// One-time backfill script: lists S3 objects for each active project and
// inserts missing rows into the tenant's storage_objects table so that
// the "Storage used" metric is accurate.
//
// Usage:
//   DATABASE_URL=... SCW_ACCESS_KEY=... SCW_SECRET_KEY=... SCW_S3_ENDPOINT=... go run ./cmd/backfill-storage
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	scwAccessKey := os.Getenv("SCW_ACCESS_KEY")
	scwSecretKey := os.Getenv("SCW_SECRET_KEY")
	scwEndpoint := os.Getenv("SCW_S3_ENDPOINT")
	scwRegion := os.Getenv("SCW_S3_REGION")

	if scwAccessKey == "" || scwSecretKey == "" || scwEndpoint == "" {
		log.Fatal("SCW_ACCESS_KEY, SCW_SECRET_KEY, and SCW_S3_ENDPOINT are required")
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect to db: %v", err)
	}
	defer pool.Close()

	s3Client, err := storage.NewS3Client(scwEndpoint, scwRegion, scwAccessKey, scwSecretKey)
	if err != nil {
		log.Fatalf("create s3 client: %v", err)
	}

	// Get all active projects.
	rows, err := pool.Query(ctx,
		`SELECT id, slug, schema_name FROM projects WHERE status = 'active'`)
	if err != nil {
		log.Fatalf("query projects: %v", err)
	}
	defer rows.Close()

	type project struct {
		id, slug, schema string
	}
	var projects []project
	for rows.Next() {
		var p project
		if err := rows.Scan(&p.id, &p.slug, &p.schema); err != nil {
			log.Fatalf("scan project: %v", err)
		}
		projects = append(projects, p)
	}
	rows.Close()

	for _, p := range projects {
		bucket := "eurobase-" + p.slug
		fmt.Printf("\n--- %s (bucket=%s, schema=%s) ---\n", p.slug, bucket, p.schema)

		exists, err := s3Client.BucketExists(ctx, bucket)
		if err != nil {
			fmt.Printf("  skip: bucket check failed: %v\n", err)
			continue
		}
		if !exists {
			fmt.Printf("  skip: bucket does not exist\n")
			continue
		}

		// List all objects in the bucket.
		var allObjects []storage.ObjectInfo
		token := ""
		for {
			result, err := s3Client.ListObjects(ctx, bucket, "", 1000, token)
			if err != nil {
				fmt.Printf("  error listing objects: %v\n", err)
				break
			}
			allObjects = append(allObjects, result.Objects...)
			if !result.IsTruncated {
				break
			}
			token = result.NextToken
		}

		if len(allObjects) == 0 {
			fmt.Printf("  no objects in bucket\n")
			continue
		}

		fmt.Printf("  found %d objects in S3\n", len(allObjects))

		escSchema := strings.ReplaceAll(p.schema, `"`, `""`)
		inserted := 0
		for _, obj := range allObjects {
			contentType := "application/octet-stream"
			q := fmt.Sprintf(
				`INSERT INTO "%s".storage_objects (key, content_type, size_bytes)
				 VALUES ($1, $2, $3)
				 ON CONFLICT (key) DO UPDATE SET size_bytes = $3`,
				escSchema,
			)
			if _, err := pool.Exec(ctx, q, obj.Key, contentType, obj.Size); err != nil {
				fmt.Printf("  error inserting %s: %v\n", obj.Key, err)
				continue
			}
			inserted++
		}

		fmt.Printf("  backfilled %d/%d objects\n", inserted, len(allObjects))
	}

	fmt.Println("\nDone.")
}
