package cli

// #269 (part of #267): `eurobase import supabase schema` — pull DDL
// from a Supabase project, translate to Eurobase conventions, and
// write a migration file the tenant can inspect + apply.
//
// The CLI shells out to `pg_dump` (must be on PATH) for the actual
// dump. Rewriting a pg_dump output is far more reliable than
// reconstructing DDL from information_schema — pg_dump knows the
// dialect exactly, and its output is easy to translate line-by-line
// (which is exactly what Translate does).
//
// Default output: `./migrations/<epoch>_from_supabase.sql`. The tenant
// runs `eurobase migrations up` after reviewing. Passing `--apply`
// runs it in one shot via the tenant-migrations endpoint (#190).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func importSupabaseSchemaCmd() *cobra.Command {
	var (
		outputPath    string
		migrationsDir string
		migName       string
	)
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Translate a Supabase project's DDL and write it as a Eurobase migration",
		Long: `Dump the schema from a Supabase project (public + storage), translate
it to Eurobase conventions, and write it as a migration file.

Env vars:
  SUPABASE_DB_URL   postgres:// URL of the Supabase project (required)

Prerequisites:
  pg_dump on PATH (installed with the postgresql-client package on most
  systems). The CLI shells out to run "pg_dump --schema-only".

Translations applied:
  - SET search_path / CREATE SCHEMA public — stripped
  - REFERENCES auth.users(id) → REFERENCES users(id)
  - auth.uid() → auth_uid()
  - auth.role() → CASE-based Eurobase equivalent
  - auth.email() → auth_email()
  - storage.objects → storage_objects
  - CREATE POLICY bodies wrapped in "public.is_service_role() OR (...)"
    so service-key writes keep working

Anything the translator can't safely rewrite (auth.jwt() reads,
references to Supabase-only schemas) is emitted as a comment above the
line and printed to stdout as a warning. Review before applying.

By default writes to ./migrations/<epoch>_from_supabase.sql — the
tenant runs "eurobase migrations up" to apply. Pass --output to change
the file path or --output - to write to stdout.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL := os.Getenv("SUPABASE_DB_URL")
			if dbURL == "" {
				return fmt.Errorf("SUPABASE_DB_URL is required (postgres:// URL of the Supabase project)")
			}
			if _, err := exec.LookPath("pg_dump"); err != nil {
				return fmt.Errorf("pg_dump not found on PATH — install postgresql-client and rerun")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			// Dump public + storage. `--no-owner --no-privileges`
			// because ownership and grants translate poorly (the
			// Eurobase tenant migrator role owns everything).
			// `--schema-only` because rows are handled by #270.
			dumpArgs := []string{
				"--schema-only",
				"--no-owner",
				"--no-privileges",
				"--no-tablespaces",
				"--schema=public",
				"--schema=storage",
				dbURL,
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Running pg_dump (schema-only, public + storage)...")
			dump, err := exec.CommandContext(ctx, "pg_dump", dumpArgs...).Output()
			if err != nil {
				// Surface pg_dump's stderr in the wrapped error so
				// the tenant sees the real reason (auth failed,
				// SSL required, etc.).
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return fmt.Errorf("pg_dump failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
				}
				return fmt.Errorf("pg_dump failed: %w", err)
			}

			result := Translate(string(dump))

			// Print warnings before writing anything so the tenant
			// sees them even if they run with --output - and pipe
			// stdout.
			if len(result.warnings) > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "")
				fmt.Fprintln(cmd.ErrOrStderr(), "── Translator warnings — review these before applying: ──")
				for _, w := range result.warnings {
					fmt.Fprintf(cmd.ErrOrStderr(), "  line %d: %s\n", w.line, w.note)
				}
				fmt.Fprintln(cmd.ErrOrStderr(), "")
			}

			// Print rewrite summary — a quick "changed N of X" line so
			// the tenant sees what the translator actually did.
			if len(result.rewrites) > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "── Translator applied: ──")
				for rule, count := range result.rewrites {
					fmt.Fprintf(cmd.ErrOrStderr(), "  %s (%d)\n", rule, count)
				}
				fmt.Fprintln(cmd.ErrOrStderr(), "")
			}

			// Prepend a translation header so a human opening the
			// file sees when and how it was produced.
			header := fmt.Sprintf(
				"-- Auto-translated from Supabase pg_dump by `eurobase import supabase schema`.\n"+
					"-- Generated: %s UTC\n"+
					"-- Review the translator warnings printed to stderr before applying.\n"+
					"-- Command to apply: eurobase migrations up\n\n",
				time.Now().UTC().Format(time.RFC3339),
			)
			payload := header + result.sql

			// Decide where to write.
			if outputPath == "-" {
				_, err := cmd.OutOrStdout().Write([]byte(payload))
				return err
			}
			path := outputPath
			if path == "" {
				if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
					return fmt.Errorf("create %s: %w", migrationsDir, err)
				}
				fname := fmt.Sprintf("%d_%s.sql", time.Now().Unix(), migName)
				path = filepath.Join(migrationsDir, fname)
			}
			if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
				return fmt.Errorf("write migration file: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Migration written to %s\n", path)
			fmt.Fprintln(cmd.OutOrStdout(), "Next: `eurobase migrations up` to apply.")
			return nil
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output path (default ./migrations/<epoch>_from_supabase.sql; use '-' for stdout)")
	cmd.Flags().StringVar(&migrationsDir, "migrations-dir", "./migrations", "Directory to write the migration file")
	cmd.Flags().StringVar(&migName, "name", "from_supabase", "Migration file basename (before .sql)")
	return cmd
}

