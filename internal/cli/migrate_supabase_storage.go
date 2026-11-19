package cli

// `eurobase import supabase storage` — copy a Supabase project's files
// into a Eurobase project.
//
// Without --apply it reports what there is (buckets, files, sizes, what
// becomes of public buckets and storage policies) and changes nothing.
// With --apply it copies: see migrate_supabase_storage_copy.go. The copy
// can be stopped and run again; files already copied are skipped.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
		apply        bool
		parallel     int
		missingOwner string
	)
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Copy a Supabase project's files (storage buckets) into the active project",
		Long: `Copy the files of a Supabase project into the active Eurobase project.

Without --apply, report what there is and change nothing: buckets, file
counts and sizes, and what happens to each bucket.

With --apply, copy every file. Each file is streamed from Supabase straight
to the project's bucket (nothing is stored on this machine), a few at a
time. The copy can be stopped and run again: files already copied are
skipped.

How files come over:
  - A Supabase bucket becomes a folder: avatars/u1/me.png stays
    avatars/u1/me.png. (storage.from("avatars") in a function built with
    the Supabase compatibility layer uses the same folder.)
  - A file keeps its owner, the end user who uploaded it, so they keep
    the access they had. Import the users first (import supabase data).
    A file whose owner is not a user of the project is imported as a
    developer file (--missing-owner skip leaves it out).
  - The files of a public bucket become developer files in a folder
    shared with everyone, because on Supabase anyone can read every file
    of a public bucket. Their owners can no longer replace or delete
    them from the app.
  - Supabase's storage policies are not carried over; on Eurobase each
    end user owns their files, and folders can be shared.

Running it again copies only what is missing. It never replaces a file
the project has: a file changed, replaced or deleted in the project after
the import is listed or — when deleted — copied again; delete such files
on Supabase first if they must stay gone.

Limits: one file can be up to 5 GB. The copy is not checked against the
plan's storage limit: compare the total in the report first.

Env vars:
  SUPABASE_DB_URL             the Supabase database (lists the files)
  SUPABASE_URL                https://<ref>.supabase.co  (with --apply)
  SUPABASE_SERVICE_ROLE_KEY   reads the files            (with --apply)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			dbURL := os.Getenv("SUPABASE_DB_URL")
			if dbURL == "" {
				return fmt.Errorf("SUPABASE_DB_URL is required (postgres:// URL of the Supabase project)")
			}
			if missingOwner != "developer" && missingOwner != "skip" {
				return fmt.Errorf("--missing-owner takes developer or skip")
			}
			ctx := cmd.Context()
			connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			conn, err := pgx.Connect(connectCtx, dbURL)
			cancel()
			if err != nil {
				return fmt.Errorf("connect to Supabase: %w", err)
			}
			defer conn.Close(context.Background())

			buckets, err := listSupabaseBuckets(ctx, conn)
			if err != nil {
				return fmt.Errorf("enumerate buckets: %w", err)
			}
			if len(buckets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No buckets on the source project: nothing to copy.")
				return nil
			}
			if targetBucket != "" {
				available := joinBucketNames(buckets)
				filtered := make([]supabaseBucket, 0, 1)
				for _, b := range buckets {
					if b.name == targetBucket {
						filtered = append(filtered, b)
					}
				}
				if len(filtered) == 0 {
					return fmt.Errorf("bucket %q not found on source (available: %s)", targetBucket, available)
				}
				buckets = filtered
			}

			if !apply {
				report, err := buildStorageReport(ctx, conn, buckets)
				if err != nil {
					return fmt.Errorf("build report: %w", err)
				}
				if outputPath == "-" || outputPath == "" {
					_, err := io.WriteString(cmd.OutOrStdout(), report)
					return err
				}
				if err := os.WriteFile(outputPath, []byte(report), 0o644); err != nil {
					return fmt.Errorf("write report: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Storage report written to %s\n", outputPath)
				return nil
			}

			base := strings.TrimSpace(os.Getenv("SUPABASE_URL"))
			key := strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY"))
			if base == "" || key == "" {
				return errors.New("--apply reads the files through Supabase's storage API: set SUPABASE_URL (https://<ref>.supabase.co) and SUPABASE_SERVICE_ROLE_KEY")
			}
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}
			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}
			// Large files: no overall time limit on a single transfer.
			transfer := &http.Client{Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 5 * time.Minute,
				MaxIdleConnsPerHost:   parallel * 2,
			}}
			src := &supabaseStorageSource{base: base, key: key, client: transfer}
			dst := &apiStorageTarget{client: client, projectID: cfg.ActiveProject}
			return runStorageCopy(ctx, cmd.OutOrStdout(), conn, buckets, src, dst, copyOptions{
				Parallel:     parallel,
				MissingOwner: missingOwner,
				Upload:       transfer,
			}, ProjectLabel(cfg))
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Without --apply: write the report to this file (default stdout)")
	cmd.Flags().StringVar(&targetBucket, "bucket", "", "Only this bucket (default: all buckets)")
	cmd.Flags().BoolVar(&apply, "apply", false, "Copy the files into the active project")
	cmd.Flags().IntVar(&parallel, "parallel", 4, "With --apply: files copied at the same time")
	cmd.Flags().StringVar(&missingOwner, "missing-owner", "developer", "With --apply: a file whose owner isn't a user of the project is imported as a developer file (developer) or left out (skip)")
	return cmd
}

// runStorageCopy copies every file of the given buckets.
func runStorageCopy(ctx context.Context, out io.Writer, q sourceQuerier, buckets []supabaseBucket, src storageSource, dst storageTarget, opts copyOptions, target string) error {
	if opts.Parallel < 1 || opts.Parallel > 32 {
		return fmt.Errorf("--parallel takes 1 to 32")
	}
	opts.PublicBuckets = map[string]bool{}
	var names []string
	for _, b := range buckets {
		// The bucket's name becomes a folder name.
		if !isSafeBucketName(b.name) {
			return fmt.Errorf("bucket %q has characters a folder name can't have; copy the others with --bucket", b.name)
		}
	}
	for _, b := range buckets {
		names = append(names, b.name)
		if b.public {
			opts.PublicBuckets[b.name] = true
		}
	}
	stats := &copyStats{}
	// Public buckets: share their folder with everyone before the first
	// file arrives — but only a folder that is new to the project. Sharing
	// a folder that already holds files would make those public too.
	if err := sharePublicFolders(buckets, dst, stats); err != nil {
		return err
	}

	fmt.Fprintf(out, "Copying into %s\n", target)
	start := time.Now()
	opts.Progress = func(s copyStats) {
		fmt.Fprintf(out, "  %d files looked at: %d copied (%s), %d already there, %d left out, %d failed — %s\n",
			s.Seen, s.Copied, formatBytes(s.Bytes), s.Already, s.Skipped, s.Failed, time.Since(start).Round(time.Second))
	}
	afterBucket, afterName := "", ""
	for {
		files, err := listSourceFiles(ctx, q, names, afterBucket, afterName, importBatchSize)
		if err != nil {
			return fmt.Errorf("list files: %w", err)
		}
		if len(files) == 0 {
			break
		}
		last := files[len(files)-1]
		afterBucket, afterName = last.Bucket, last.Name
		var batch []sourceFile
		for _, f := range files {
			// Supabase's placeholder for an empty folder; folders don't
			// need one here.
			if f.Name == ".emptyFolderPlaceholder" || strings.HasSuffix(f.Name, "/.emptyFolderPlaceholder") {
				continue
			}
			batch = append(batch, f)
		}
		if len(batch) > 0 {
			if err := copyFiles(ctx, batch, src, dst, opts, stats); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	fmt.Fprintf(out, "\nDone: %d copied (%s), %d already there, %d left out, %d failed.\n",
		stats.Copied, formatBytes(stats.Bytes), stats.Already, stats.Skipped, stats.Failed)
	if len(stats.SharedFolders) > 0 {
		fmt.Fprintf(out, "Shared with everyone (public buckets on Supabase): %s\n", strings.Join(stats.SharedFolders, ", "))
	}
	if stats.OwnerDropped > 0 {
		fmt.Fprintf(out, "%d file(s) were imported as developer files (in a public bucket, or their owner isn't a user of the project).\n", stats.OwnerDropped)
	}
	if len(stats.Problems) > 0 {
		fmt.Fprintln(out, "\nNot copied:")
		for _, p := range stats.Problems {
			fmt.Fprintf(out, "  - %s\n", p)
		}
	}
	if stats.Failed > 0 {
		return fmt.Errorf("%d file(s) failed; run the command again to retry them (copied files are skipped)", stats.Failed)
	}
	return nil
}

// sharePublicFolders shares each public bucket's folder with everyone,
// once: a folder already shared with everyone is left as it is; one
// shared otherwise, or one that already holds files the import didn't
// put there, stops the copy before anything is changed.
func sharePublicFolders(buckets []supabaseBucket, dst storageTarget, stats *copyStats) error {
	var public []supabaseBucket
	for _, b := range buckets {
		if b.public {
			public = append(public, b)
		}
	}
	if len(public) == 0 {
		return nil
	}
	rules, err := dst.SharedFolders()
	if err != nil {
		return fmt.Errorf("read the project's shared folders: %w", err)
	}
	var share []string
	for _, b := range public {
		prefix := b.name + "/"
		if strings.EqualFold(b.name, "exports") {
			return fmt.Errorf("the public bucket %q can't become a folder: exports/ holds the project's data exports — copy the other buckets with --bucket, and this one by hand into another folder", b.name)
		}
		switch v, ok := rules[prefix]; {
		case ok && v == "public":
			continue // shared by an earlier run
		case ok:
			return fmt.Errorf("the folder %s is shared with %s users; the public bucket %q needs it shared with everyone — change the rule in the console (Storage → Sharing) if that is what you want, then run again", prefix, v, b.name)
		}
		has, err := dst.HasFiles(prefix)
		if err != nil {
			return fmt.Errorf("look at the folder %s: %w", prefix, err)
		}
		if has {
			return fmt.Errorf("the project already has files in %s; sharing that folder with everyone (as the public bucket %q was) would make them public too — move them, or share the folder yourself (Storage → Sharing) if they may be public, then run again", prefix, b.name)
		}
		share = append(share, prefix)
	}
	for _, prefix := range share {
		if err := dst.Share(prefix, "public"); err != nil {
			return fmt.Errorf("share the folder %s with everyone: %w", prefix, err)
		}
		stats.SharedFolders = append(stats.SharedFolders, prefix)
	}
	return nil
}

// importBatchSize matches the platform's limit per request.
const importBatchSize = 100

// listSourceFiles returns the next files after (afterBucket, afterName),
// in byte order, from the given buckets.
func listSourceFiles(ctx context.Context, q sourceQuerier, buckets []string, afterBucket, afterName string, limit int) ([]sourceFile, error) {
	// The whole row as JSON: the owner column is "owner" (uuid) on older
	// projects and "owner_id" (text) on newer ones.
	rows, err := q.Query(ctx, `
		SELECT o.bucket_id, o.name,
		       COALESCE(o.metadata ->> 'size', ''), COALESCE(o.metadata ->> 'mimetype', ''),
		       COALESCE(to_jsonb(o) ->> 'owner_id', to_jsonb(o) ->> 'owner', '')
		  FROM storage.objects o
		 WHERE o.bucket_id = ANY($1)
		   AND (o.bucket_id COLLATE "C", o.name COLLATE "C") > ($2 COLLATE "C", $3 COLLATE "C")
		 ORDER BY o.bucket_id COLLATE "C", o.name COLLATE "C"
		 LIMIT $4`, buckets, afterBucket, afterName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sourceFile
	for rows.Next() {
		var f sourceFile
		var size string
		if err := rows.Scan(&f.Bucket, &f.Name, &size, &f.ContentType, &f.Owner); err != nil {
			return nil, err
		}
		f.Size = -1
		if n, err := parseSize(size); err == nil {
			f.Size = n
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func parseSize(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int64(c-'0')
		if n > 1<<50 {
			return 0, errors.New("too large")
		}
	}
	return n, nil
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
// bytes are populated by buildStorageReport.
func listSupabaseBuckets(ctx context.Context, q sourceQuerier) ([]supabaseBucket, error) {
	rows, err := q.Query(ctx, `
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
func buildStorageReport(ctx context.Context, q sourceQuerier, buckets []supabaseBucket) (string, error) {
	totals, err := storageObjectTotals(ctx, q)
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
	var grandCount, grandBytes int64
	for _, b := range buckets {
		grandCount += b.objectCount
		grandBytes += b.totalBytes
	}
	fmt.Fprintf(&out, "Total objects: %s across %s\n\n", formatCount(grandCount), formatBytes(grandBytes))
	for _, b := range buckets {
		out.WriteString(renderBucketSection(b))
	}
	fmt.Fprintln(&out, "## Copying")
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "Run the command again with `--apply` (and `SUPABASE_URL`, `SUPABASE_SERVICE_ROLE_KEY`) to copy the files into the active project. Each file is streamed from Supabase straight to the project's bucket; the copy can be stopped and run again. Import the users first (`import supabase data`), so files keep their owners.")
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "## Storage policies")
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "Supabase's policies on `storage.objects` are not carried over. On Eurobase each end user owns the files they upload, and folders can be shared with signed-in users or everyone (console → Storage → Sharing). Check that this covers what your policies allowed.")
	return out.String(), nil
}

// renderBucketSection returns the Markdown block for one bucket.
//
// All tenant-controlled strings (bucket name, MIME types) run through
// mdEscape so a hostile source can't inject structural Markdown into
// the report.
func renderBucketSection(b supabaseBucket) string {
	var out strings.Builder
	fmt.Fprintf(&out, "## Bucket: **%s**\n\n", mdEscape(b.name))
	visibility := "private — files keep their owners"
	if b.public {
		visibility = "public — copied as developer files into a folder shared with everyone"
	}
	fmt.Fprintf(&out, "- Visibility: %s\n", visibility)
	fmt.Fprintf(&out, "- Becomes the folder: %s/\n", mdEscape(b.name))
	fmt.Fprintf(&out, "- Objects: %s\n", formatCount(b.objectCount))
	fmt.Fprintf(&out, "- Size:    %s\n", formatBytes(b.totalBytes))
	if b.fileSizeLimit != nil && *b.fileSizeLimit > 0 {
		fmt.Fprintf(&out, "- File size limit: %s per object (not carried over)\n", formatBytes(*b.fileSizeLimit))
	}
	if len(b.allowedMimeTypes) > 0 {
		escaped := make([]string, len(b.allowedMimeTypes))
		for i, m := range b.allowedMimeTypes {
			escaped[i] = mdEscape(m)
		}
		fmt.Fprintf(&out, "- Allowed MIME types: %s (not carried over)\n", strings.Join(escaped, ", "))
	}
	if b.objectCount > 0 && b.totalBytes == 0 {
		fmt.Fprintln(&out, "- **Note:** the size shows 0 because `metadata->>'size'` isn't filled in on this project's storage.objects (older Supabase projects). The file count is right; sizes are read during the copy.")
	}
	fmt.Fprintln(&out)
	return out.String()
}

// isSafeBucketName returns true if the name matches Supabase's actual
// bucket-name grammar: `[a-zA-Z0-9-_.]+`.
func isSafeBucketName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

type storageTotal struct {
	count int64
	bytes int64
}

// storageObjectTotals returns per-bucket object counts and byte sums.
func storageObjectTotals(ctx context.Context, q sourceQuerier) (map[string]storageTotal, error) {
	rows, err := q.Query(ctx, `
		SELECT bucket_id,
		       COUNT(*) AS n,
		       COALESCE(SUM(CASE WHEN metadata ->> 'size' ~ '^[0-9]+$' THEN (metadata ->> 'size')::bigint END), 0) AS total_bytes
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
