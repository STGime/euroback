package cli

// #268 (part of #267): `eurobase import supabase assess` — read-only
// reconnaissance of a Supabase project. Enumerates tables, RLS, auth
// users, storage buckets, functions, extensions, and grades each item:
//
//   ✅ can migrate     — a later phase (schema / data / storage /
//                       functions) will handle it automatically
//   ⚠  needs review   — will migrate but the tenant should confirm
//                       (e.g. an edge function importing an npm
//                       package; a policy with unusual jwt-claim usage)
//   ❌ blocker        — Eurobase can't support this and the tenant
//                       needs a workaround (pg_graphql, custom
//                       Postgres extensions, etc.)
//
// Produces a Markdown report the tenant can eyeball before committing
// to the actual migration. Read-only by design — nothing is written to
// either Supabase or Eurobase.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"
)

const (
	gradeOK      = "✅"
	gradeWarn    = "⚠"
	gradeBlocker = "❌"
)

// item is one row in the assess report.
type item struct {
	name  string // human-readable name (e.g. "auth.users", "table: orders")
	grade string // gradeOK / gradeWarn / gradeBlocker
	note  string // one-line explanation, especially for warn/blocker
}

// report is what assess produces before rendering. Kept as a struct
// (not a stream) so we can print in a stable order regardless of the
// query order.
type report struct {
	sourceURL   string
	targetHint  string // where to point the tenant for the next step
	generatedAt time.Time

	tables     []item
	policies   []item
	authUsers  []item
	storage    []item
	functions  []item
	extensions []item
	dbCode     []item // database functions + triggers
	views      []item // views, enums, sequences
	realtime   []item
	blockers   []item // any ❌ found anywhere, hoisted for the summary
}

func importSupabaseAssessCmd() *cobra.Command {
	var (
		outputPath   string
		functionsDir string
	)
	cmd := &cobra.Command{
		Use:   "assess",
		Short: "Read-only compat report against a Supabase project",
		Long: `Enumerate what's in a Supabase project and produce a compatibility
report. Read-only — nothing is written to either Supabase or Eurobase.

Env vars:
  SUPABASE_DB_URL  postgres:// URL of the Supabase project (required).
                   Tables, RLS policies, auth users, storage buckets and
                   Postgres extensions are all read from here — no
                   service-role key needed.

Edge functions live in your repo, not the database. Point --functions-dir
at your local Supabase project's functions folder (default
./supabase/functions) to grade them; if it's absent, that section is
skipped with a note.

The report grades every item:
  ✅ can migrate  — a later phase handles it automatically
  ⚠  needs review — will migrate but the tenant should confirm
  ❌ blocker     — Eurobase can't support this today

Output: ./supabase-migration-report-<UTC-timestamp>.md by default so a
rerun doesn't overwrite the previous report. Pass --output to override
(use '-' for stdout).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// From here on errors are about the run, not the flags.
			cmd.SilenceUsage = true
			dbURL := os.Getenv("SUPABASE_DB_URL")
			if dbURL == "" {
				return fmt.Errorf("SUPABASE_DB_URL is required (postgres:// URL of the Supabase project)")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			conn, err := pgx.Connect(ctx, dbURL)
			if err != nil {
				return fmt.Errorf("connect to Supabase: %w%s", err, supabaseConnectHint(err))
			}
			defer conn.Close(ctx)

			r := &report{
				sourceURL:   redactURL(dbURL),
				targetHint:  "Run `eurobase import supabase schema` next, after addressing the blockers above.",
				generatedAt: time.Now().UTC(),
			}

			if err := assessTables(ctx, conn, r); err != nil {
				return fmt.Errorf("enumerate tables: %w", err)
			}
			if err := assessPolicies(ctx, conn, r); err != nil {
				return fmt.Errorf("enumerate RLS policies: %w", err)
			}
			grants, err := readNarrowedGrants(ctx, conn)
			if err != nil {
				return fmt.Errorf("enumerate privileges: %w", err)
			}
			r.policies = append(r.policies, gradeGrants(grants)...)
			if err := assessAuthUsers(ctx, conn, r); err != nil {
				return fmt.Errorf("enumerate auth users: %w", err)
			}
			code, err := readSupabaseCode(ctx, conn)
			if err != nil {
				return fmt.Errorf("enumerate functions, triggers and views: %w", err)
			}
			r.dbCode = append(gradeFunctions(code.functions), gradeTriggers(code.triggers)...)
			r.views = gradeViews(code.views, code.enumCount, code.sequenceCount)
			r.realtime = gradeRealtime(code.realtimeAll, code.realtimeTabs)
			if err := assessExtensions(ctx, conn, r, code); err != nil {
				return fmt.Errorf("enumerate extensions: %w", err)
			}
			// Storage buckets live in the Postgres `storage` schema, so
			// we read them over the same connection — no service key.
			if err := assessStorage(ctx, conn, r); err != nil {
				return fmt.Errorf("enumerate storage buckets: %w", err)
			}
			// Edge functions live in the tenant's repo (./supabase/
			// functions/<name>/index.ts), not the database — graded by
			// running the same translator the `functions` step uses.
			if err := assessFunctions(r, functionsDir); err != nil {
				return fmt.Errorf("enumerate edge functions: %w", err)
			}

			// Hoist all blockers into the summary section.
			for _, group := range [][]item{r.tables, r.views, r.policies, r.dbCode, r.authUsers, r.storage, r.realtime, r.functions, r.extensions} {
				for _, it := range group {
					if it.grade == gradeBlocker {
						r.blockers = append(r.blockers, it)
					}
				}
			}

			path := outputPath
			if path == "" {
				// Timestamped default so a rerun after a partial
				// migration doesn't clobber the previous report
				// (review #268 M3). Format is stable and sortable.
				path = fmt.Sprintf("./supabase-migration-report-%s.md",
					r.generatedAt.Format("2006-01-02T15-04-05Z"))
			}
			var out io.Writer
			if path == "-" {
				out = cmd.OutOrStdout()
			} else {
				f, err := os.Create(path)
				if err != nil {
					return fmt.Errorf("create output file: %w", err)
				}
				defer f.Close()
				out = f
			}
			if err := writeReport(out, r); err != nil {
				return fmt.Errorf("write report: %w", err)
			}
			if path != "-" {
				fmt.Fprintf(cmd.OutOrStdout(), "Report written to %s\n", path)
				if len(r.blockers) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %d blocker(s) found — review the report before proceeding.\n", gradeBlocker, len(r.blockers))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Report output path (default ./supabase-migration-report-<UTC-timestamp>.md; use '-' for stdout)")
	cmd.Flags().StringVar(&functionsDir, "functions-dir", "./supabase/functions", "Local Supabase edge-functions directory to grade (skipped if absent)")
	return cmd
}

// ── enumerators ──────────────────────────────────────────────────────

func assessTables(ctx context.Context, conn *pgx.Conn, r *report) error {
	// public.* tables only for now. Supabase can have custom schemas
	// but the vast majority of tenants put everything in public — a
	// tenant with custom schemas will need to run assess per schema
	// (documented in the report).
	rows, err := conn.Query(ctx, `
		SELECT c.relname AS table_name,
		       COALESCE(s.n_live_tup, 0) AS row_count,
		       pg_total_relation_size(c.oid) AS bytes,
		       c.relrowsecurity,
		       CASE WHEN EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon')
		            THEN ARRAY(SELECT p FROM unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE']) p
		                       WHERE has_table_privilege('anon', c.oid, p))
		            ELSE ARRAY[]::text[] END AS anon_privs
		FROM   pg_class c
		JOIN   pg_namespace n ON n.oid = c.relnamespace
		LEFT   JOIN pg_stat_user_tables s
		         ON s.schemaname = n.nspname AND s.relname = c.relname
		WHERE  n.nspname = 'public'
		  AND  c.relkind = 'r'
		  AND  NOT EXISTS (SELECT 1 FROM pg_depend d
		                   WHERE d.classid = 'pg_class'::regclass
		                     AND d.objid = c.oid AND d.deptype = 'e')
		ORDER BY c.relname
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var rowCount int64
		var bytes int64
		var rls bool
		var anonPrivs []string
		if err := rows.Scan(&name, &rowCount, &bytes, &rls, &anonPrivs); err != nil {
			return err
		}
		r.tables = append(r.tables, gradeTable(name, rowCount, bytes, rls, anonPrivs))
	}
	return rows.Err()
}

// gradeTable grades one public table. A table without RLS is a ⚠: on
// Eurobase every request with the project's API keys could read and
// write it, and on Supabase the anon key usually can already.
func gradeTable(name string, rowCount, bytes int64, rls bool, anonPrivs []string) item {
	it := item{
		name:  fmt.Sprintf("public.%s", name),
		grade: gradeOK,
		note:  fmt.Sprintf("%s rows · %s", formatCount(rowCount), formatBytes(bytes)),
	}
	if !rls {
		it.grade = gradeWarn
		it.note += " · RLS is disabled: enable it and add policies before your app uses this table on Eurobase"
		if len(anonPrivs) > 0 {
			it.note += fmt.Sprintf(". On Supabase today, anyone with your anon key can %s this table; worth fixing there now", strings.Join(anonPrivs, " / "))
			if q, ok := sqlName("public", name); ok {
				it.note += " (`ALTER TABLE " + q + " ENABLE ROW LEVEL SECURITY`)"
			}
		}
	}
	return it
}

func assessPolicies(ctx context.Context, conn *pgx.Conn, r *report) error {
	rows, err := conn.Query(ctx, `
		SELECT schemaname || '.' || tablename AS on_table,
		       polname,
		       pg_get_expr(polqual, polrelid) AS using_clause,
		       pg_get_expr(polwithcheck, polrelid) AS with_check
		FROM   pg_policy p
		JOIN   pg_class c ON c.oid = p.polrelid
		JOIN   pg_namespace n ON n.oid = c.relnamespace
		JOIN   pg_tables t ON t.schemaname = n.nspname AND t.tablename = c.relname
		WHERE  schemaname IN ('public', 'storage')
		ORDER BY on_table, polname
	`)
	if err != nil {
		// Fall back to the pg_policies view only when the failure is
		// a permission/definition problem — anything else (context
		// cancelled, network drop, syntax error introduced by a
		// future edit) is a real error and must surface. This was
		// the #268 review's H2 finding.
		if isFallbackWorthy(err) {
			return assessPoliciesSimple(ctx, conn, r)
		}
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var onTable, polname string
		var usingClause, withCheck *string
		if err := rows.Scan(&onTable, &polname, &usingClause, &withCheck); err != nil {
			return err
		}
		note, grade := gradePolicy(usingClause, withCheck)
		if strings.HasPrefix(onTable, "storage.") {
			note, grade = storagePolicyNote, gradeWarn
		}
		r.policies = append(r.policies, item{
			name:  fmt.Sprintf("%s :: %s", onTable, polname),
			grade: grade,
			note:  note,
		})
	}
	return rows.Err()
}

func assessPoliciesSimple(ctx context.Context, conn *pgx.Conn, r *report) error {
	rows, err := conn.Query(ctx, `
		SELECT schemaname || '.' || tablename AS on_table, policyname
		FROM   pg_policies
		WHERE  schemaname IN ('public', 'storage')
		ORDER BY on_table, policyname
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var onTable, name string
		if err := rows.Scan(&onTable, &name); err != nil {
			return err
		}
		note := "policy body not readable from this Postgres role; check its translation in the `schema` step's output"
		if strings.HasPrefix(onTable, "storage.") {
			note = storagePolicyNote
		}
		r.policies = append(r.policies, item{
			name:  fmt.Sprintf("%s :: %s", onTable, name),
			grade: gradeWarn,
			note:  note,
		})
	}
	return rows.Err()
}

// storagePolicyNote is the grade note for policies on Supabase's
// storage tables: Eurobase doesn't use storage policies.
const storagePolicyNote = "storage policies aren't migrated: Eurobase gives each end user their own files automatically, plus shared folders (authenticated or public) you set in the console. Remove this policy from the `schema` step's output and set up shared folders where you need them"

func gradePolicy(usingClause, withCheck *string) (string, string) {
	// Collapse both clauses into one lower-cased blob for grading.
	buf := ""
	if usingClause != nil {
		buf += *usingClause
	}
	if withCheck != nil {
		buf += " " + *withCheck
	}
	lower := strings.ToLower(buf)

	// The common cases translate cleanly.
	// auth.uid() → auth_uid(), auth.role() → helper wrap.
	if lower == "" {
		return "empty policy body — check post-migration", gradeWarn
	}
	if strings.Contains(lower, "auth.jwt()") && strings.Contains(lower, "app_metadata") {
		return "policy reads app_metadata from the JWT: Eurobase tokens don't carry it, so check the `schema` step's output and rewrite it (e.g. a role column on a profile table)", gradeWarn
	}
	if strings.Contains(lower, "auth.jwt()") && strings.Contains(lower, "user_metadata") {
		return "policy reads user_metadata from the JWT: Eurobase tokens don't carry it, so check the `schema` step's output and rewrite it", gradeWarn
	}
	if strings.Contains(lower, "auth.jwt()") {
		return "policy reads auth.jwt(): Eurobase doesn't expose the raw token to SQL. Rewrite it with auth.uid() / auth.email() / auth.role() or a lookup in your own tables", gradeWarn
	}
	if strings.Contains(lower, "auth.email()") {
		return "auth.email() is translated to Eurobase's auth_email() by the `schema` step", gradeOK
	}
	if strings.Contains(lower, "auth.uid()") || strings.Contains(lower, "auth.role()") {
		return "auth.uid() / auth.role() are translated by the `schema` step", gradeOK
	}
	// A policy body that touches neither auth.* nor storage.* is
	// likely fine but worth a look.
	return "no auth.* references; copied as-is by the `schema` step", gradeOK
}

func assessAuthUsers(ctx context.Context, conn *pgx.Conn, r *report) error {
	var count int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM auth.users`).Scan(&count); err != nil {
		// Only treat this as "table missing" for schema/table/
		// privilege errors — a network drop or context cancel is
		// a real failure and must surface (review #268 H3).
		if isMissingObjectError(err) {
			r.authUsers = append(r.authUsers, item{
				name:  "auth.users",
				grade: gradeWarn,
				note:  "table not accessible or missing (unusual; expected on any GoTrue-enabled project)",
			})
			return nil
		}
		return fmt.Errorf("query auth.users: %w", err)
	}
	r.authUsers = append(r.authUsers, item{
		name:  "auth.users",
		grade: gradeOK,
		note:  fmt.Sprintf("%s users · bcrypt password_hashes copy directly, UUIDs preserved so RLS still works after migration", formatCount(count)),
	})

	// Enumerate active OAuth providers from auth.identities. Some
	// Supabase versions expose auth.providers as a separate view; try
	// identities first (more portable).
	rows, err := conn.Query(ctx, `
		SELECT DISTINCT provider FROM auth.identities WHERE provider IS NOT NULL ORDER BY provider
	`)
	if err != nil {
		return nil // best-effort — identities may not be readable
	}
	defer rows.Close()
	var providers []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		providers = append(providers, p)
	}
	sort.Strings(providers)
	// Three tiers (#268 review M1):
	//   supported          — Eurobase ships this provider today
	//   knownSupabaseOnly  — Supabase ships it, we don't; users have
	//                        to re-federate via a supported provider
	//                        (⚠ not ❌ — most tenants know their
	//                        options and can re-federate cleanly)
	//   anything else      — unknown provider name, worth flagging
	//                        for a manual look
	supported := map[string]bool{
		"email":         true, // GoTrue records email/phone as pseudo-providers
		"phone":         true,
		"google":        true,
		"github":        true,
		"linkedin":      true, // legacy provider name; Supabase kept it around
		"linkedin_oidc": true, // Supabase's post-2024 LinkedIn provider — NOT a typo
		"apple":         true,
		"azure":         true,
		"microsoft":     true,
	}
	knownSupabaseOnly := map[string]bool{
		"facebook":   true,
		"twitter":    true,
		"discord":    true,
		"gitlab":     true,
		"bitbucket":  true,
		"slack":      true,
		"slack_oidc": true,
		"spotify":    true,
		"twitch":     true,
		"notion":     true,
		"figma":      true,
		"kakao":      true,
		"keycloak":   true,
		"workos":     true,
		"zoom":       true,
		"fly":        true,
	}
	for _, p := range providers {
		switch {
		case supported[p]:
			r.authUsers = append(r.authUsers, item{
				name:  fmt.Sprintf("auth provider: %s", p),
				grade: gradeOK,
				note:  "Eurobase supports this provider natively",
			})
		case knownSupabaseOnly[p]:
			r.authUsers = append(r.authUsers, item{
				name:  fmt.Sprintf("auth provider: %s", p),
				grade: gradeWarn,
				note:  "Supabase supports this but Eurobase does not yet — affected users re-federate via a supported provider on next sign-in",
			})
		default:
			r.authUsers = append(r.authUsers, item{
				name:  fmt.Sprintf("auth provider: %s", p),
				grade: gradeWarn,
				note:  "unknown provider name — verify what this is before proceeding",
			})
		}
	}
	return nil
}

func assessExtensions(ctx context.Context, conn *pgx.Conn, r *report, code *sbCode) error {
	rows, err := conn.Query(ctx, `
		SELECT extname FROM pg_extension WHERE extname NOT IN ('plpgsql', 'plpython3u') ORDER BY extname
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var exts []string
	for rows.Next() {
		var ext string
		if err := rows.Scan(&ext); err != nil {
			return err
		}
		exts = append(exts, ext)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	r.extensions = gradeExtensions(exts, code)
	return nil
}

// gradeExtensions grades installed extensions. Supabase installs several
// in every project (pg_net, supabase_vault, pg_graphql, ...), so those
// are graded by whether the project actually uses them, not by being
// installed.
func gradeExtensions(exts []string, code *sbCode) []item {
	if code == nil {
		code = &sbCode{vaultSecrets: -1}
	}
	pgNetUse, vaultUse, sodiumUse := code.extensionUse()
	notUsed := "installed by Supabase by default and not used by your functions, triggers or cron jobs; nothing to migrate"

	// Available on Eurobase's shared database.
	available := map[string]string{
		"pgcrypto":  "available on Eurobase",
		"uuid-ossp": "available on Eurobase (gen_random_uuid(), built into Postgres, is preferred for new columns)",
	}
	// Not available, no workaround inside the database.
	blockers := map[string]string{
		"vector":      "pgvector is not available on Eurobase today; contact support before migrating a project that depends on it",
		"http":        "Eurobase has no HTTP from SQL; call external services from an edge function",
		"plv8":        "the V8 procedural language is not available; rewrite these functions in plpgsql or as edge functions",
		"timescaledb": "TimescaleDB is not available; contact support before migrating",
		"wrappers":    "foreign data wrappers are not available",
	}
	warns := map[string]string{
		"pg_graphql":    "installed by Supabase by default. Only matters if your app calls the GraphQL endpoint (/graphql/v1); Eurobase has no GraphQL, so rewrite those calls with the SDK",
		"pgjwt":         "installed by default on older Supabase projects. Only matters if your SQL calls sign() / verify(); Eurobase issues its own tokens",
		"pg_jsonschema": "not available; validate JSON in application code or edge functions",
		"pgaudit":       "Eurobase's audit log covers platform actions but there's no pgaudit output",
		"pg_tle":        "trusted-language extensions are not available; move that logic to app code",
		"pgmq":          "no Postgres message queue on Eurobase; use an application queue or a cron-driven edge function",
		"pg_repack":     "not needed on Eurobase (managed maintenance); check any scripts that call it",
		"postgis":       "PostGIS is not available by default; contact support if you rely on it",
	}
	var out []item
	for _, ext := range exts {
		it := item{name: ext}
		switch {
		case ext == "pg_net":
			// Database webhooks are already listed as blockers of their
			// own; only other uses make pg_net itself one.
			var other []string
			for _, u := range pgNetUse {
				if !strings.HasPrefix(u, "database webhook ") {
					other = append(other, u)
				}
			}
			switch {
			case len(other) > 0:
				it.grade, it.note = gradeBlocker, "Eurobase has no HTTP from SQL. Used by: "+nameList(other)+". Use Eurobase webhooks / function triggers, or call an edge function from your app"
			case len(pgNetUse) > 0:
				it.grade, it.note = gradeOK, "used only by the database webhooks listed above; nothing else to migrate"
			default:
				it.grade, it.note = gradeOK, notUsed
			}
		case ext == "supabase_vault":
			switch {
			case len(vaultUse) > 0:
				it.grade, it.note = gradeBlocker, "Eurobase Vault can't be read from SQL. Used by: "+nameList(vaultUse)+". Move that code into an edge function, which can read Vault secrets"
			case code.vaultSecrets > 0:
				it.grade, it.note = gradeWarn, fmt.Sprintf("%d secret(s) in Supabase Vault: copy them to Eurobase Vault (`eurobase vault set`); they aren't migrated automatically", code.vaultSecrets)
			case code.vaultSecrets == 0:
				it.grade, it.note = gradeOK, "installed by Supabase by default, no secrets stored; nothing to migrate"
			default:
				it.grade, it.note = gradeWarn, "couldn't read vault.secrets; if you store secrets there, copy them to Eurobase Vault (`eurobase vault set`)"
			}
		case ext == "pgsodium":
			if len(sodiumUse) > 0 {
				it.grade, it.note = gradeBlocker, "pgsodium is not available. Used by: "+nameList(sodiumUse)+". Use Eurobase Vault or do the cryptography in an edge function"
			} else {
				it.grade, it.note = gradeOK, "installed by default on older Supabase projects and not used by your functions; nothing to migrate"
			}
		case ext == "pg_cron":
			if code.cronUnknown {
				it.grade, it.note = gradeWarn, "couldn't read cron.job with this login; if you have cron jobs, recreate them as Eurobase cron jobs (SQL or a database function)"
			} else if len(code.cronJobs) > 0 {
				it.grade, it.note = gradeWarn, fmt.Sprintf("%d cron job(s) (%s): recreate them as Eurobase cron jobs (SQL or a database function); they aren't migrated automatically", len(code.cronJobs), nameList(code.cronJobs))
			} else {
				it.grade, it.note = gradeOK, "no cron jobs visible to this login (jobs created by another database role don't show); nothing to migrate"
			}
		case ext == "pg_stat_statements":
			it.grade, it.note = gradeOK, "Supabase's query statistics; not needed on Eurobase, nothing to migrate"
		case available[ext] != "":
			it.grade, it.note = gradeOK, available[ext]
		case blockers[ext] != "":
			it.grade, it.note = gradeBlocker, blockers[ext]
		case warns[ext] != "":
			it.grade, it.note = gradeWarn, warns[ext]
		default:
			it.grade, it.note = gradeWarn, "not installed on Eurobase by default; ask support before relying on it"
		}
		out = append(out, it)
	}
	return out
}

// assessStorage enumerates the project's storage buckets from the
// Postgres `storage` schema (same read-only source the `storage` step
// uses — no service-role key). One item per bucket with its visibility,
// object count and total size; a multi-bucket project gets a ⚠ because
// Eurobase runs one bucket per project today (migrates as a
// prefix-per-bucket plan, see the `storage` step). If the storage schema
// isn't present/readable the section is skipped with a note rather than
// failing the whole run.
func assessStorage(ctx context.Context, conn *pgx.Conn, r *report) error {
	buckets, err := listSupabaseBuckets(ctx, conn)
	if err != nil {
		if isMissingObjectError(err) {
			r.storage = append(r.storage, item{
				name:  "storage.buckets",
				grade: gradeWarn,
				note:  "storage schema not accessible — skipped (present on any Supabase project with Storage enabled)",
			})
			return nil
		}
		return err
	}
	if len(buckets) == 0 {
		return nil // writeSection renders "(none found)"
	}
	// Object counts + sizes are a best-effort enrichment: if
	// storage.objects can't be read we still list the buckets.
	if totals, err := storageObjectTotals(ctx, conn); err == nil {
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
	} else if !isMissingObjectError(err) {
		return err
	}
	for _, b := range buckets {
		visibility := "private"
		if b.public {
			visibility = "public"
		}
		note := fmt.Sprintf("%s · %s objects · %s", visibility, formatCount(b.objectCount), formatBytes(b.totalBytes))
		if b.objectCount > 0 && b.totalBytes == 0 {
			// Older Supabase schemas don't populate metadata->>'size';
			// the object count is accurate, the byte total isn't — say
			// so rather than let a tenant read "0 B" as "empty".
			note += " (size unknown — metadata->>'size' not populated on this project; objects still copy)"
		}
		if b.public {
			note += " — public buckets map to Eurobase's public shared-prefix mechanism; review after migrating"
		}
		r.storage = append(r.storage, item{
			name:  fmt.Sprintf("bucket: %s", b.name),
			grade: gradeOK,
			note:  note,
		})
	}
	if len(buckets) > 1 {
		r.storage = append(r.storage, item{
			name:  fmt.Sprintf("%d buckets total", len(buckets)),
			grade: gradeWarn,
			note:  "Eurobase currently runs one bucket per project; a multi-bucket layout migrates as a prefix-per-bucket plan (see `eurobase import supabase storage`)",
		})
	}
	return nil
}

// assessFunctions grades the edge functions in the tenant's local
// Supabase project directory. Functions aren't in Postgres — they live
// in ./supabase/functions/<name>/ — so we run each through the same
// TranslateFunction the `functions` step uses and report whether it
// ports cleanly, ports with warnings, or needs a manual rewrite first.
// A missing directory is a skip-with-note, not an error (a tenant may be
// assessing before checking out their functions).
func assessFunctions(r *report, functionsDir string) error {
	info, err := os.Stat(functionsDir)
	if err != nil {
		// Only "not found" is a skip-with-note — a real stat failure
		// (e.g. a permission error on an existing path) must surface
		// rather than being disguised as "point --functions-dir somewhere".
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err != nil || !info.IsDir() {
		r.functions = append(r.functions, item{
			name:  "(skipped — no local functions directory)",
			grade: gradeWarn,
			note:  fmt.Sprintf("edge functions live in your repo, not the database; point --functions-dir at your Supabase functions folder (looked in %s) to grade them", functionsDir),
		})
		return nil
	}
	entries, err := os.ReadDir(functionsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Supabase convention: a leading `_` is a shared module, not a
		// deployable function; dotfiles (.git, .DS_Store) aren't either.
		if strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		entryFile := resolveEntrypoint(filepath.Join(functionsDir, e.Name()))
		source, err := os.ReadFile(filepath.Join(functionsDir, e.Name(), entryFile))
		if err != nil {
			r.functions = append(r.functions, item{
				name:  fmt.Sprintf("function: %s", e.Name()),
				grade: gradeWarn,
				note:  fmt.Sprintf("no readable %s — non-standard entry point, review by hand", entryFile),
			})
			continue
		}
		res := TranslateFunction(string(source))
		switch {
		case res.Unsupported:
			r.functions = append(r.functions, item{
				name:  fmt.Sprintf("function: %s", e.Name()),
				grade: gradeWarn,
				note:  fmt.Sprintf("needs a manual rewrite before deploy — %s", res.UnsupportedReason),
			})
		case len(res.Warnings) > 0:
			r.functions = append(r.functions, item{
				name:  fmt.Sprintf("function: %s", e.Name()),
				grade: gradeWarn,
				note:  fmt.Sprintf("ports with %d warning(s) to review (e.g. remote imports or dynamic env access)", len(res.Warnings)),
			})
		default:
			r.functions = append(r.functions, item{
				name:  fmt.Sprintf("function: %s", e.Name()),
				grade: gradeOK,
				note:  "ports cleanly to Eurobase's runner contract",
			})
		}
	}
	return nil
}

// ── report writer ────────────────────────────────────────────────────

func writeReport(w io.Writer, r *report) error {
	buf := &strings.Builder{}
	fmt.Fprintf(buf, "# Supabase → Eurobase migration report\n\n")
	fmt.Fprintf(buf, "> Source: `%s`\n", r.sourceURL)
	fmt.Fprintf(buf, "> Generated: %s UTC\n\n", r.generatedAt.Format(time.RFC3339))

	if len(r.blockers) > 0 {
		fmt.Fprintf(buf, "## %s Blockers — %d found\n\n", gradeBlocker, len(r.blockers))
		fmt.Fprintf(buf, "Change these before importing: the import refuses them, or they don't carry over.\n\n")
		for _, it := range r.blockers {
			fmt.Fprintf(buf, "- **%s** — %s\n", mdEscape(it.name), mdNote(it.note))
		}
		fmt.Fprintf(buf, "\n")
	} else {
		fmt.Fprintf(buf, "## %s No blockers — this project looks migratable end-to-end.\n\n", gradeOK)
	}

	writeSection(buf, "Tables", r.tables)
	writeSection(buf, "Views and types", r.views)
	writeSection(buf, "RLS policies", r.policies)
	writeSection(buf, "Database functions and triggers", r.dbCode)
	writeSection(buf, "Auth users + providers", r.authUsers)
	writeSection(buf, "Storage", r.storage)
	writeSection(buf, "Realtime", r.realtime)
	writeSection(buf, "Edge functions", r.functions)
	writeSection(buf, "Postgres extensions", r.extensions)

	fmt.Fprintf(buf, "## Next step\n\n%s\n", r.targetHint)

	_, err := io.WriteString(w, buf.String())
	return err
}

func writeSection(buf *strings.Builder, title string, items []item) {
	fmt.Fprintf(buf, "## %s\n\n", title)
	if len(items) == 0 {
		fmt.Fprintf(buf, "_(none found)_\n\n")
		return
	}
	for _, it := range items {
		fmt.Fprintf(buf, "- %s **%s** — %s\n", it.grade, mdEscape(it.name), mdNote(it.note))
	}
	fmt.Fprintf(buf, "\n")
}

// ── small helpers ────────────────────────────────────────────────────

func formatCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

func formatBytes(n int64) string {
	const kb = 1024
	if n < kb {
		return fmt.Sprintf("%d B", n)
	}
	if n < kb*kb {
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	}
	if n < kb*kb*kb {
		return fmt.Sprintf("%.1f MB", float64(n)/(kb*kb))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(kb*kb*kb))
}

// redactURL strips the password from a postgres:// URL so it's safe to
// embed in the report. Uses net/url so a properly percent-encoded
// password (the only kind libpq accepts) round-trips cleanly no matter
// where the literal `@` characters land. See #268 review L1.
func redactURL(raw string) string {
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return raw
	}
	// url.UserPassword percent-encodes the sentinel because `*` is
	// reserved in userinfo. Use a URL-safe sentinel then swap it back
	// to the visually-obvious `***` in the final string. This keeps
	// the report readable without relying on tenants recognising
	// `%2A%2A%2A`.
	const safeSentinel = "REDACTED"
	u.User = url.UserPassword(u.User.Username(), safeSentinel)
	return strings.Replace(u.String(), ":"+safeSentinel+"@", ":***@", 1)
}

// mdEscape returns a Markdown-safe rendering of a name that may
// contain characters with formatting meaning (backticks, asterisks,
// underscores, brackets, angle brackets, pipes) or control chars.
// Postgres identifiers are legal up to 63 bytes with pretty much any
// content, so a table named `orders**\n\n# Fake Section` would
// otherwise inject fake structure into the report we're about to hand
// the tenant. Review #268 H1.
//
// Escape strategy: backslash-escape the six Markdown metacharacters
// tenants are most likely to hit; replace newlines with a visible
// literal `↩`; drop any other control chars. NBSP and unicode
// direction overrides are kept as-is (they render fine in every
// Markdown viewer I could name; we're mostly defending against
// structural injection, not perfect fidelity).
func mdEscape(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '`', '*', '_', '[', ']', '<', '>', '|', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n', '\r':
			b.WriteString("↩")
		default:
			if r < 0x20 && r != '\t' {
				continue // drop other control chars silently
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mdNote renders a grade note. Notes are written by assess itself and
// may contain `code spans`; identifiers inside them go through ident()
// (no backticks), so everything except backticks is escaped as in
// mdEscape.
func mdNote(s string) string {
	parts := strings.Split(s, "`")
	for i := range parts {
		if i%2 == 1 {
			// Inside a code span: keep as-is apart from line breaks.
			parts[i] = strings.NewReplacer("\n", " ", "\r", " ").Replace(parts[i])
			continue
		}
		parts[i] = mdEscape(parts[i])
	}
	if len(parts)%2 == 0 {
		// Unbalanced backtick: escape the whole note instead.
		return mdEscape(s)
	}
	return strings.Join(parts, "`")
}

// supabaseConnectHint adds a hint for the connection errors people hit
// with Supabase URLs. It never repeats the URL.
func supabaseConnectHint(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "tenant/user") && strings.Contains(msg, "not found"):
		return "\nHint: the pooler doesn't know this project. Copy the Session pooler URL from the Supabase dashboard (Connect): the user is postgres.<project-ref> and the host is aws-0-… or aws-1-…<region>.pooler.supabase.com"
	case strings.Contains(msg, "28p01") || strings.Contains(msg, "password authentication failed"):
		return "\nHint: wrong database password (Supabase dashboard → Project Settings → Database). URL-encode special characters in it"
	case strings.Contains(msg, "supabase.co") && (strings.Contains(msg, "no such host") || strings.Contains(msg, "lookup") || strings.Contains(msg, "network is unreachable")):
		return "\nHint: the direct db.<ref>.supabase.co host is IPv6-only. Use the Session pooler URL from the Supabase dashboard (Connect) instead"
	}
	return ""
}

// isFallbackWorthy reports whether an error from the primary
// assessPolicies query is one the fallback (pg_policies view) might
// handle. Only permission / undefined-object errors qualify —
// otherwise the caller must surface the real failure. Review #268 H2.
func isFallbackWorthy(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42501", // insufficient_privilege
		"42883", // undefined_function (e.g. pg_get_expr signature diff)
		"42P01": // undefined_table
		return true
	}
	return false
}

// isMissingObjectError reports whether an error can be attributed to
// a missing schema/table/privilege on the target object. Used by
// enumerators (assessAuthUsers) to distinguish "this project doesn't
// have auth.users, skip the section" from "the connection died".
func isMissingObjectError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "3F000", // schema not found
		"42P01", // undefined_table
		"42501": // insufficient_privilege (safe to treat as "missing" for reporting purposes)
		return true
	}
	return false
}

// gradeGrants turns GRANT / REVOKE restrictions into blockers: Eurobase
// has no per-role grants, so each must be replaced (RLS, a view, an edge
// function) before the app goes live there.
func gradeGrants(gs []narrowedGrant) []item {
	out := make([]item, 0, len(gs))
	for _, g := range gs {
		out = append(out, item{
			name:  "grant: " + g.object + " (" + g.role + ")",
			grade: gradeBlocker,
			note:  g.role + " has " + g.detail + " on Supabase. " + grantsNote,
		})
	}
	return out
}
