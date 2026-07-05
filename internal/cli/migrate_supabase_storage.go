package cli

// #271 (part of #267): `eurobase import supabase storage` — enumerate
// Supabase buckets, produce a summary report, and print the `rclone`
// commands the tenant runs to copy the bytes over.
//
// Scope note: the umbrella issue #271 mentions extending Eurobase's
// storage model to support multiple buckets per project (currently one
// bucket-per-project). That platform work — DB migration adding a
// `bucket` column to `storage_objects`, gateway routing by bucket,
// SDK `.bucket('name')` accessor — is a SEPARATE follow-up PR. This
// PR ships the CLI-side extraction so tenants can start moving bytes;
// once multi-bucket ships, a later revision of this command will also
// emit policy translations mapping `bucket_id` → `bucket`.
//
// Approach — same shape as `assess` (#268) and `schema` (#269): the
// CLI does read-only enumeration + emits shell commands. The tenant
// runs the actual byte transfer manually (`rclone` on PATH). Nothing
// automatic against the target Eurobase project — the tenant reviews
// the plan and executes.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
)

func importSupabaseStorageCmd() *cobra.Command {
	var (
		outputPath   string
		targetBucket string
		showRclone   bool
	)
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Enumerate Supabase buckets and print the rclone commands to copy the bytes",
		Long: `Read the bucket layout from a Supabase project (via its Postgres
storage.buckets + storage.objects tables) and produce a plan the tenant
can act on:

  1. A summary report per bucket: size, object count, public/private,
     file-size limit, allowed MIME types.
  2. The exact ` + "`rclone sync`" + ` command the tenant runs to copy the
     bytes from Supabase's S3-compatible endpoint into Eurobase's
     Scaleway bucket.

Env vars:
  SUPABASE_DB_URL   postgres:// URL of the Supabase project (required
                    — enumerates storage.buckets + storage.objects)

Prerequisites (documented in the report footer):
  rclone on PATH.

  Two rclone remotes need to be configured — the tenant sets these up
  once, then reruns the printed commands. Suggested names:
    supabase_src   — the source project's S3-compatible endpoint.
                     Region: any (Supabase uses "us-east-1").
                     Endpoint: https://<project>.supabase.co/storage/v1/s3
                     Provider: "Other" / S3-compatible.
                     Access key + secret: the service-role key pair
                     from the Supabase console (Storage → API keys).
    eurobase_dst   — the target Eurobase project's Scaleway bucket.
                     Region: fr-par (or your project's region).
                     Endpoint: https://s3.fr-par.scw.cloud
                     Provider: Scaleway.
                     Access key + secret: from the Eurobase console
                     (Storage → Credentials → S3-compatible).

## Multi-bucket scope note

Supabase supports arbitrarily many buckets per project. Eurobase today
runs one bucket per project. Translating multi-bucket layouts into
Eurobase natively is a platform-side change (DB migration + gateway
routing + SDK accessor) shipping in a later PR. In the interim, this
command:

  - Reports every source bucket individually.
  - Prints one ` + "`rclone sync`" + ` command per bucket, targeting
    ` + "`eurobase_dst:/<bucket-name>/`" + ` — the tenant gets the same
    prefix layout once multi-bucket lands.

Pass --bucket <name> to filter to a single source bucket. --hide-rclone
suppresses the rclone commands (report only).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL := os.Getenv("SUPABASE_DB_URL")
			if dbURL == "" {
				return fmt.Errorf("SUPABASE_DB_URL is required (postgres:// URL of the Supabase project)")
			}
			if showRclone {
				if _, err := exec.LookPath("rclone"); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(),
						"warning: rclone not found on PATH — install it (`brew install rclone` / `apt install rclone`) before running the printed commands")
				}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			conn, err := pgx.Connect(ctx, dbURL)
			if err != nil {
				return fmt.Errorf("connect to Supabase: %w", err)
			}
			defer conn.Close(ctx)

			buckets, err := listSupabaseBuckets(ctx, conn)
			if err != nil {
				return fmt.Errorf("enumerate buckets: %w", err)
			}
			if len(buckets) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "No buckets found on the source Supabase project. Nothing to copy.")
				return nil
			}
			if targetBucket != "" {
				filtered := buckets[:0]
				for _, b := range buckets {
					if b.name == targetBucket {
						filtered = append(filtered, b)
					}
				}
				buckets = filtered
				if len(buckets) == 0 {
					return fmt.Errorf("bucket %q not found on source (available: %s)", targetBucket, joinBucketNames(buckets))
				}
			}

			report, err := buildStorageReport(ctx, conn, buckets, showRclone)
			if err != nil {
				return fmt.Errorf("build report: %w", err)
			}

			if outputPath == "-" || outputPath == "" {
				_, err := cmd.OutOrStdout().Write([]byte(report))
				if err != nil {
					return err
				}
				return nil
			}
			if err := os.WriteFile(outputPath, []byte(report), 0o644); err != nil {
				return fmt.Errorf("write report: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Storage report written to %s\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output path (default stdout; use a filename to save)")
	cmd.Flags().StringVar(&targetBucket, "bucket", "", "Report only this bucket (default: all buckets)")
	cmd.Flags().BoolVar(&showRclone, "rclone", true, "Include the rclone sync commands in the output")
	return cmd
}

// supabaseBucket is the projected shape of one row from Supabase's
// storage.buckets table. Only the fields we actually surface in the
// report.
type supabaseBucket struct {
	name             string
	public           bool
	fileSizeLimit    *int64 // bytes; nullable
	allowedMimeTypes []string
	objectCount      int64
	totalBytes       int64
}

// listSupabaseBuckets reads the bucket rows. Object counts + total
// bytes are populated by buildStorageReport (a separate scan against
// storage.objects since we don't want to lock buckets on a heavy
// aggregate).
func listSupabaseBuckets(ctx context.Context, conn *pgx.Conn) ([]supabaseBucket, error) {
	rows, err := conn.Query(ctx, `
		SELECT id, public, file_size_limit, allowed_mime_types
		  FROM storage.buckets
		 ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []supabaseBucket
	for rows.Next() {
		var b supabaseBucket
		var mime []string
		if err := rows.Scan(&b.name, &b.public, &b.fileSizeLimit, &mime); err != nil {
			return nil, err
		}
		b.allowedMimeTypes = mime
		out = append(out, b)
	}
	return out, rows.Err()
}

// buildStorageReport writes the Markdown report for `buckets`.
// Populates object counts + sizes per bucket by querying
// storage.objects grouped by bucket_id.
func buildStorageReport(ctx context.Context, conn *pgx.Conn, buckets []supabaseBucket, showRclone bool) (string, error) {
	totals, err := storageObjectTotals(ctx, conn)
	if err != nil {
		return "", err
	}
	byName := make(map[string]int, len(buckets))
	for i, b := range buckets {
		byName[b.name] = i
	}
	for name, t := range totals {
		if idx, ok := byName[name]; ok {
			buckets[idx].objectCount = t.count
			buckets[idx].totalBytes = t.bytes
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "# Supabase → Eurobase storage plan\n\n")
	fmt.Fprintf(&out, "Generated: %s UTC\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&out, "Buckets: %d\n\n", len(buckets))

	// Grand total.
	var grandCount int64
	var grandBytes int64
	for _, b := range buckets {
		grandCount += b.objectCount
		grandBytes += b.totalBytes
	}
	fmt.Fprintf(&out, "Total objects: %s across %s\n\n", formatCount(grandCount), formatBytes(grandBytes))

	// Per-bucket rows.
	for _, b := range buckets {
		fmt.Fprintf(&out, "## Bucket: `%s`\n\n", mdEscape(b.name))
		visibility := "private"
		if b.public {
			visibility = "public"
		}
		fmt.Fprintf(&out, "- Visibility: %s\n", visibility)
		fmt.Fprintf(&out, "- Objects: %s\n", formatCount(b.objectCount))
		fmt.Fprintf(&out, "- Size:    %s\n", formatBytes(b.totalBytes))
		if b.fileSizeLimit != nil && *b.fileSizeLimit > 0 {
			fmt.Fprintf(&out, "- File size limit: %s per object\n", formatBytes(*b.fileSizeLimit))
		}
		if len(b.allowedMimeTypes) > 0 {
			fmt.Fprintf(&out, "- Allowed MIME types: `%s`\n", strings.Join(b.allowedMimeTypes, "`, `"))
		}
		fmt.Fprintln(&out)
		if showRclone {
			fmt.Fprintf(&out, "```\n%s\n```\n\n", rcloneCommandFor(b))
		}
	}

	// Footer: rclone setup + next-step hints.
	fmt.Fprintln(&out, "## Prerequisites")
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "1. Install `rclone` (`brew install rclone` / `apt install rclone`).")
	fmt.Fprintln(&out, "2. Configure two remotes with `rclone config`:")
	fmt.Fprintln(&out, "   - `supabase_src` → the Supabase project's S3-compatible endpoint")
	fmt.Fprintln(&out, "     (`https://<project>.supabase.co/storage/v1/s3`).")
	fmt.Fprintln(&out, "   - `eurobase_dst` → the Eurobase project's Scaleway bucket")
	fmt.Fprintln(&out, "     (`https://s3.fr-par.scw.cloud`; credentials from the Eurobase console).")
	fmt.Fprintln(&out, "3. Run the commands above; each is idempotent (`rclone sync` skips objects already at the destination with the same size + checksum).")
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "## Multi-bucket note")
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "Eurobase currently exposes one bucket per project. Each `rclone sync` above targets `eurobase_dst:/<bucket-name>/`, so once Eurobase's multi-bucket support ships (planned follow-up in umbrella #267), your object layout already matches — no re-sync needed.")
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "## Storage policies")
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "Supabase per-bucket policies on `storage.objects` are NOT translated by this command — Eurobase's single-bucket policy model doesn't have a one-to-one mapping until multi-bucket lands. Review your `storage.objects` policies in the `assess` report and plan a re-implementation with Eurobase's storage policy DSL after the byte copy completes.")
	return out.String(), nil
}

// rcloneCommandFor returns the rclone invocation for one bucket. Uses
// --progress so the tenant sees a live meter, and --transfers 8 as a
// reasonable default for parallel object copies.
func rcloneCommandFor(b supabaseBucket) string {
	return fmt.Sprintf(
		"rclone sync --progress --transfers 8 \\\n"+
			"  supabase_src:%s \\\n"+
			"  eurobase_dst:%s",
		b.name, b.name,
	)
}

type storageTotal struct {
	count int64
	bytes int64
}

// storageObjectTotals returns per-bucket object counts + byte sums via
// one aggregate scan of storage.objects. Cheaper than N queries when a
// project has many buckets. Missing metadata.size falls back to 0 (the
// count is still accurate).
func storageObjectTotals(ctx context.Context, conn *pgx.Conn) (map[string]storageTotal, error) {
	rows, err := conn.Query(ctx, `
		SELECT bucket_id,
		       COUNT(*)                                          AS n,
		       COALESCE(SUM((metadata ->> 'size')::bigint), 0)   AS total_bytes
		  FROM storage.objects
		 GROUP BY bucket_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]storageTotal{}
	for rows.Next() {
		var name string
		var t storageTotal
		if err := rows.Scan(&name, &t.count, &t.bytes); err != nil {
			return nil, err
		}
		out[name] = t
	}
	return out, rows.Err()
}

// joinBucketNames renders a comma-separated list of bucket names for
// an error message. Sorted for stable output.
func joinBucketNames(bs []supabaseBucket) string {
	names := make([]string, len(bs))
	for i, b := range bs {
		names[i] = b.name
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
