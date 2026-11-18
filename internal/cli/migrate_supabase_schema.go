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
// reviews it and applies it with "eurobase migrations up".

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
)

func importSupabaseSchemaCmd() *cobra.Command {
	var (
		outputPath    string
		migrationsDir string
		migName       string
		acceptAccess  bool
	)
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Translate a Supabase project's DDL and write it as a Eurobase migration",
		Long: `Dump the schema of a Supabase project's public schema, translate it
to Eurobase conventions, and write it as a migration file.

Env vars:
  SUPABASE_DB_URL   postgres:// URL of the Supabase project (required;
                    use the Session pooler URL from Connect in the
                    Supabase dashboard — the direct host is IPv6-only)

Prerequisites:
  pg_dump on PATH, the same major version as the Supabase database or
  newer (installed with the postgresql-client package on most systems).

Translations applied:
  - pg_dump preamble (SET …, set_config, \restrict) — stripped
  - public.<name> → <name> (the migration runs in your project schema)
  - REFERENCES auth.users(id) → REFERENCES users(id)
  - auth.uid() → auth_uid(), auth.email() → auth_email(),
    auth.role() → an equivalent CASE ('service_role' / 'anon' /
    'authenticated') — in policies and in function bodies
  - policies TO anon / authenticated / service_role → a condition
    (Eurobase has no such database roles), and every policy body
    wrapped in "public.is_service_role() OR (...)" so service-key
    requests keep full access, as with Supabase's service_role
  - SECURITY DEFINER functions → run with the caller's rights
  - views → security_invoker (they apply the caller's RLS)
  - extensions, Realtime publications, materialized views — commented
    out with the reason

Supabase's own schemas (auth, storage, realtime, …) aren't copied:
users come over with "import supabase data", files with "import
supabase storage". Run "import supabase assess" first — it lists what
needs a rewrite (auth triggers, SECURITY DEFINER functions, webhooks).

Everything the translator changed or couldn't translate is printed as a
warning. Review the file before applying it.

Some differences change who can do what, and the file isn't written
until you accept them with --accept-access-changes:
  - restrictions made with GRANT / REVOKE (column-level security,
    tables or functions closed to anon) — Eurobase has no per-role
    grants, only RLS
  - checks on current_user / current_setting('role') / request.jwt.*
  - policies or triggers that call a former SECURITY DEFINER function
  - policies, RLS switches or triggers that had to be left out

By default writes to ./migrations/<epoch>_from_supabase.sql — the
tenant runs "eurobase migrations up" to apply. Pass --output to change
the file path or --output - to write to stdout.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			dbURL := os.Getenv("SUPABASE_DB_URL")
			if dbURL == "" {
				return fmt.Errorf("SUPABASE_DB_URL is required (postgres:// URL of the Supabase project)")
			}
			if _, err := exec.LookPath("pg_dump"); err != nil {
				return fmt.Errorf("pg_dump not found on PATH — install postgresql-client and rerun")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			// Dump public only: Supabase's storage schema is its own
			// implementation (buckets, multipart uploads, lifecycle
			// functions) — files move with "import supabase storage"
			// and Eurobase storage has its own access rules.
			// `--no-owner --no-privileges` because ownership and grants
			// are the platform's. `--schema-only` because rows are
			// handled by "import supabase data".
			dumpArgs := []string{
				"--schema-only",
				"--no-owner",
				"--no-privileges",
				"--no-tablespaces",
				"--schema=public",
				dbURL,
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Running pg_dump (schema-only, public schema)...")
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

			// GRANT / REVOKE restrictions (column-level security, tables
			// or functions closed to anon) don't carry over — read them
			// from the source, since the dump leaves privileges out.
			grants, err := sourceNarrowedGrants(ctx, dbURL)
			if err != nil {
				return err
			}

			var access, other []translationWarning
			for _, w := range result.warnings {
				if w.access {
					access = append(access, w)
				} else {
					other = append(other, w)
				}
			}
			errw := cmd.ErrOrStderr()
			printWarnings := func(ws []translationWarning) {
				for _, w := range ws {
					if w.line > 0 {
						fmt.Fprintf(errw, "  line %d: %s\n", w.line, w.note)
					} else {
						fmt.Fprintf(errw, "  - %s\n", w.note)
					}
				}
			}

			// Print warnings before writing anything so the tenant
			// sees them even if they run with --output - and pipe
			// stdout.
			if len(other) > 0 {
				fmt.Fprintln(errw, "")
				fmt.Fprintln(errw, "── Translator warnings — review these before applying: ──")
				printWarnings(other)
				fmt.Fprintln(errw, "")
			}
			accessCount := len(access) + len(grants)
			if accessCount > 0 {
				fmt.Fprintln(errw, "── Access changes — after the import, callers may be able to do things they couldn't on Supabase: ──")
				printWarnings(access)
				if len(grants) > 0 {
					fmt.Fprintf(errw, "  - access narrowed with GRANT / REVOKE on Supabase (%d):\n", len(grants))
					for _, g := range grants {
						fmt.Fprintf(errw, "      %s\n", g)
					}
					fmt.Fprintf(errw, "    %s\n", grantsNote)
				}
				fmt.Fprintln(errw, "")
				if !acceptAccess {
					return fmt.Errorf("%d access change(s) need your review — nothing was written. Fix them on Supabase and rerun, or rerun with --accept-access-changes once you have a plan for each (the file then lists them at the top)", accessCount)
				}
			}

			// Print rewrite summary — a quick "changed N of X" line so
			// the tenant sees what the translator actually did.
			if len(result.rewrites) > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "── Translator applied: ──")
				rules := make([]string, 0, len(result.rewrites))
				for rule := range result.rewrites {
					rules = append(rules, rule)
				}
				sort.Strings(rules)
				for _, rule := range rules {
					fmt.Fprintf(cmd.ErrOrStderr(), "  %s (%d)\n", rule, result.rewrites[rule])
				}
				fmt.Fprintln(cmd.ErrOrStderr(), "")
			}

			// Prepend a translation header so a human opening the
			// file sees when and how it was produced.
			header := fmt.Sprintf(
				"-- Auto-translated from Supabase pg_dump by `eurobase import supabase schema`.\n"+
					"-- Generated: %s UTC\n"+
					"-- Review the translator warnings printed to stderr before applying.\n"+
					"-- Command to apply: eurobase migrations up\n",
				time.Now().UTC().Format(time.RFC3339),
			)
			if accessCount > 0 {
				header += "--\n-- ACCESS CHANGES (accepted with --accept-access-changes) — handle each before going live:\n"
				for _, w := range access {
					header += "--   - " + oneLine(w.note) + "\n"
				}
				for _, g := range grants {
					header += "--   - GRANT/REVOKE not carried over: " + oneLine(g.String()) + "\n"
				}
			}
			header += "\n"
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
	cmd.Flags().BoolVar(&acceptAccess, "accept-access-changes", false, "Write the migration even though some access rules can't be carried over (they're listed at the top of the file)")
	return cmd
}

// sourceNarrowedGrants reads the GRANT / REVOKE restrictions from the
// Supabase database. Fails closed: if they can't be read, the command
// stops rather than write a migration that may open access silently.
func sourceNarrowedGrants(ctx context.Context, dbURL string) ([]narrowedGrant, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("connect to read privileges: %w", err)
	}
	defer conn.Close(ctx)
	return readNarrowedGrants(ctx, conn)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
