// One-time backfill: reconciles S3 object listings with the
// per-tenant `storage_objects` tracking table.
//
// Why this exists: every download/delete/preview path checks
// `storage_objects` (not S3). A row that's in S3 but not in the
// table 404s on every op — the file appears in the list (which reads
// S3 directly) but nothing else works. Two known causes:
//
//   1. Day-1 FK-violation on the console upload path (fixed in
//      internal/storage/handler.go — uploaded_by now stores NULL for
//      platform uploads). Every console-uploaded file since day 1 is
//      an orphan; this script re-creates the missing rows.
//
//   2. Manual S3 uploads (rare) — same effect.
//
// Idempotent: `INSERT ... ON CONFLICT (key) DO NOTHING`, so re-runs
// are safe and don't clobber legitimately-updated rows.
//
// Usage:
//   # every active project
//   DATABASE_URL=... SCW_ACCESS_KEY=... SCW_SECRET_KEY=... SCW_S3_ENDPOINT=... \
//     go run ./cmd/backfill-storage
//
//   # single project (recommended for spot-fixes)
//   PROJECT_ID=<uuid> DATABASE_URL=... SCW_ACCESS_KEY=... … go run ./cmd/backfill-storage
//
//   # preview without writing
//   DRY_RUN=1 DATABASE_URL=... … go run ./cmd/backfill-storage
package main

import (
	"context"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
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

	// Optional filter — restrict to a single project. Empty = all
	// active projects. Use env var (not argv) to match the rest of
	// scripts/ops/ conventions and keep args out of `ps`.
	projectFilter := strings.TrimSpace(os.Getenv("PROJECT_ID"))
	dryRun := os.Getenv("DRY_RUN") != ""

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect to db: %v", err)
	}
	defer pool.Close()

	s3Client, err := storage.NewS3Client(scwEndpoint, scwRegion, scwAccessKey, scwSecretKey)
	if err != nil {
		log.Fatalf("create s3 client: %v", err)
	}

	// Get target projects.
	var (
		projectQuery string
		queryArgs    []any
	)
	if projectFilter != "" {
		projectQuery = `SELECT id, slug, schema_name FROM projects WHERE status = 'active' AND id = $1`
		queryArgs = []any{projectFilter}
	} else {
		projectQuery = `SELECT id, slug, schema_name FROM projects WHERE status = 'active'`
	}
	rows, err := pool.Query(ctx, projectQuery, queryArgs...)
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

	if len(projects) == 0 {
		if projectFilter != "" {
			log.Fatalf("no active project found with id=%s", projectFilter)
		}
		log.Fatal("no active projects found")
	}

	mode := "APPLY"
	if dryRun {
		mode = "DRY_RUN (no writes)"
	}
	fmt.Printf("Mode: %s\nProjects to process: %d\n", mode, len(projects))

	var totalInserted, totalSkipped, totalErrors int
	for _, p := range projects {
		bucket := "eurobase-" + p.slug
		fmt.Printf("\n--- %s (id=%s, bucket=%s, schema=%s) ---\n", p.slug, p.id, bucket, p.schema)

		exists, err := s3Client.BucketExists(ctx, bucket)
		if err != nil {
			fmt.Printf("  skip: bucket check failed: %v\n", err)
			continue
		}
		if !exists {
			fmt.Printf("  skip: bucket does not exist\n")
			continue
		}

		// Paginate through every object in the bucket.
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
		// ON CONFLICT DO NOTHING is the load-bearing idempotency:
		// a legitimately-updated row (e.g. a user's manual size
		// correction) must not be clobbered by a backfill re-run.
		// The runtime upload path uses DO UPDATE — different
		// semantics because that path IS the source of truth for
		// content_type + size. Backfill is best-effort recovery.
		q := fmt.Sprintf(
			`INSERT INTO "%s".storage_objects (key, content_type, size_bytes, created_at)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (key) DO NOTHING`,
			escSchema,
		)

		inserted, skipped, errCount := 0, 0, 0
		for _, obj := range allObjects {
			// NFC-normalize keys we backfill so subsequent
			// lookups (which also NFC-normalize) find them.
			// If S3 stored an NFD key and we insert it NFD,
			// the lookup NFC-form won't match. Compose here.
			key := norm.NFC.String(obj.Key)
			contentType := guessContentType(key)

			if dryRun {
				fmt.Printf("    [dry] would insert key=%q content_type=%s size=%d created_at=%s\n",
					key, contentType, obj.Size, obj.LastModified.Format("2006-01-02T15:04:05Z"))
				inserted++
				continue
			}

			ct, err := pool.Exec(ctx, q, key, contentType, obj.Size, obj.LastModified)
			if err != nil {
				fmt.Printf("  error inserting %q: %v\n", key, err)
				errCount++
				continue
			}
			if ct.RowsAffected() == 0 {
				skipped++ // row already existed
			} else {
				inserted++
			}
		}

		fmt.Printf("  inserted=%d already-tracked=%d errors=%d\n", inserted, skipped, errCount)
		totalInserted += inserted
		totalSkipped += skipped
		totalErrors += errCount
	}

	fmt.Printf("\nDone. inserted=%d already-tracked=%d errors=%d\n",
		totalInserted, totalSkipped, totalErrors)
	if totalErrors > 0 {
		os.Exit(1)
	}
}

// guessContentType is a best-effort MIME lookup from the file
// extension. S3 ListObjectsV2 doesn't return ContentType (only
// HeadObject does, at 1 extra request per object), so backfill picks
// the sensible default and lets the runtime upload path be the source
// of truth for correct values. `.jpg → image/jpeg` etc. Falls back to
// application/octet-stream (opaque bytes) which is always safe.
func guessContentType(key string) string {
	ext := strings.ToLower(filepath.Ext(key))
	if ext == "" {
		return "application/octet-stream"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		// Strip the ";charset=…" parameter mime adds for text
		// types — storage_objects stores the bare media type
		// and the upload path does the same.
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			return strings.TrimSpace(ct[:i])
		}
		return ct
	}
	return "application/octet-stream"
}
