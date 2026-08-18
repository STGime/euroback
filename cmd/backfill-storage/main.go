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
		projectQuery = `SELECT id, slug, schema_name, COALESCE(plan, 'free') FROM projects WHERE status = 'active' AND id = $1`
		queryArgs = []any{projectFilter}
	} else {
		projectQuery = `SELECT id, slug, schema_name, COALESCE(plan, 'free') FROM projects WHERE status = 'active'`
	}
	rows, err := pool.Query(ctx, projectQuery, queryArgs...)
	if err != nil {
		log.Fatalf("query projects: %v", err)
	}
	defer rows.Close()

	type project struct {
		id, slug, schema, plan string
	}
	var projects []project
	for rows.Next() {
		var p project
		if err := rows.Scan(&p.id, &p.slug, &p.schema, &p.plan); err != nil {
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

		// Pre-scan: which S3 keys are non-NFC and need to be
		// renamed BEFORE we insert their tracking rows? Handled
		// separately so the counts are honest (renamed vs
		// plain-insert) and the rename failure mode is visible.
		//
		// Why the S3-side rename is load-bearing: the read path
		// NFC-normalizes the URL key AND fetches S3 with that
		// same NFC key. If S3 stored the object under an NFD
		// name (macOS Chrome multipart uploads often do this),
		// inserting an NFC tracking row makes assertObjectVisible
		// pass but DownloadObject(bucket, NFCkey) misses S3 →
		// still a 404 for the user. Only complete fix is to
		// physically rename the S3 object to NFC so both sides
		// speak the same bytes. Server-side copy + delete keeps
		// bytes off the wire.
		// WORM guard: Legal Team buckets use S3 Object Lock for
		// §257 HGB / §147 AO retention. `CopyObject` does NOT
		// inherit source-object retention — a naive rename would
		// produce an unprotected NFC copy while the locked NFD
		// original stays behind, silently defeating the WORM
		// guarantee. Until we implement retention-preserving copy
		// (Get retention on source → set ObjectLockMode +
		// ObjectLockRetainUntilDate + LegalHoldStatus on the
		// CopyObjectInput), refuse to rename on legal_team
		// projects. Report the non-NFC keys so an operator can
		// handle them manually (or wait for the retention-aware
		// path). The tracking-row insert is also skipped for those
		// specific keys — inserting the NFC row would create a
		// broken pointer since S3 still holds the NFD object.
		//
		// NFC-clean objects in legal_team buckets are unaffected
		// — normal insert path runs. This only skips the rename
		// (and the coupled insert) for the NFD subset.
		skipRenameForWORM := p.plan == "legal_team"
		inserted, skipped, errCount, renamed, renameErr, wormSkipped := 0, 0, 0, 0, 0, 0
		for _, obj := range allObjects {
			nfcKey := storage.NormalizeStorageKey(obj.Key)

			if nfcKey != obj.Key {
				if skipRenameForWORM {
					fmt.Printf("    SKIP-WORM %q would need rename to %q — legal_team bucket, retention-preserving copy not yet implemented. Handle manually.\n", obj.Key, nfcKey)
					wormSkipped++
					continue
				}
				// NFD (or otherwise non-canonical) S3 key. Rename
				// to NFC before inserting the tracking row.
				if dryRun {
					fmt.Printf("    [dry] would RENAME s3 %q → %q\n", obj.Key, nfcKey)
					renamed++
				} else {
					if err := s3Client.CopyObject(ctx, bucket, obj.Key, nfcKey); err != nil {
						fmt.Printf("  error copying %q → %q: %v\n", obj.Key, nfcKey, err)
						renameErr++
						continue // skip insert — S3 still has only the old key
					}
					if err := s3Client.DeleteObject(ctx, bucket, obj.Key); err != nil {
						// Delete failed after copy succeeded. Both
						// objects exist now — the runtime read path
						// will hit the NFC one (that's the goal), but
						// storage cost is doubled until manual
						// cleanup. Object Lock is one common cause;
						// bucket policy misconfig is another. Log
						// loudly, don't fail the whole run.
						fmt.Printf("  WARN copy ok but delete failed for %q (both keys now in S3, needs manual cleanup): %v\n", obj.Key, err)
						renameErr++
					}
					fmt.Printf("    renamed %q → %q\n", obj.Key, nfcKey)
					renamed++
				}
			}

			key := nfcKey
			contentType := guessContentType(key)

			if dryRun {
				// Distinguish "would insert new" vs "already
				// tracked" for the DRY_RUN summary — reviewer
				// flagged the pre-fix version over-counted.
				var exists bool
				existQ := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM "%s".storage_objects WHERE key = $1)`, escSchema)
				if err := pool.QueryRow(ctx, existQ, key).Scan(&exists); err != nil {
					fmt.Printf("  [dry] existence check failed for %q: %v\n", key, err)
					errCount++
					continue
				}
				if exists {
					fmt.Printf("    [dry] already tracked: %q\n", key)
					skipped++
				} else {
					fmt.Printf("    [dry] would insert key=%q content_type=%s size=%d created_at=%s\n",
						key, contentType, obj.Size, obj.LastModified.Format("2006-01-02T15:04:05Z"))
					inserted++
				}
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

		fmt.Printf("  inserted=%d already-tracked=%d renamed=%d rename-errors=%d insert-errors=%d worm-skipped=%d\n",
			inserted, skipped, renamed, renameErr, errCount, wormSkipped)
		if wormSkipped > 0 {
			fmt.Printf("  ⚠ %d non-NFC keys skipped on this WORM (legal_team) bucket. These files stay broken until the retention-preserving rename lands; see issue tracker.\n", wormSkipped)
		}
		totalInserted += inserted
		totalSkipped += skipped
		totalErrors += errCount + renameErr
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
