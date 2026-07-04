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
	"net/url"
	"os"
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
	blockers   []item // any ❌ found anywhere, hoisted for the summary
}

func importSupabaseAssessCmd() *cobra.Command {
	var outputPath string
	cmd := &cobra.Command{
		Use:   "assess",
		Short: "Read-only compat report against a Supabase project",
		Long: `Enumerate what's in a Supabase project and produce a compatibility
report. Read-only — nothing is written to either Supabase or Eurobase.

Env vars:
  SUPABASE_DB_URL      postgres:// URL of the Supabase project (required)
  SUPABASE_SERVICE_KEY the service-role key (required for storage + functions
                       enumeration; DB-only assess works without it)

The report grades every item:
  ✅ can migrate  — a later phase handles it automatically
  ⚠  needs review — will migrate but the tenant should confirm
  ❌ blocker     — Eurobase can't support this today

Output: ./supabase-migration-report-<UTC-timestamp>.md by default so a
rerun doesn't overwrite the previous report. Pass --output to override
(use '-' for stdout).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL := os.Getenv("SUPABASE_DB_URL")
			if dbURL == "" {
				return fmt.Errorf("SUPABASE_DB_URL is required (postgres:// URL of the Supabase project)")
			}
			// Service key is optional here — DB assessment works
			// without it. We warn if missing (the report will be
			// incomplete) but don't hard-fail. Storage / function
			// enumeration lives behind the service key.
			serviceKey := os.Getenv("SUPABASE_SERVICE_KEY")

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			conn, err := pgx.Connect(ctx, dbURL)
			if err != nil {
				return fmt.Errorf("connect to Supabase: %w", err)
			}
			defer conn.Close(ctx)

			r := &report{
				sourceURL:   redactURL(dbURL),
				targetHint:  "(run `eurobase import supabase schema` next after reviewing this report)",
				generatedAt: time.Now().UTC(),
			}

			if err := assessTables(ctx, conn, r); err != nil {
				return fmt.Errorf("enumerate tables: %w", err)
			}
			if err := assessPolicies(ctx, conn, r); err != nil {
				return fmt.Errorf("enumerate RLS policies: %w", err)
			}
			if err := assessAuthUsers(ctx, conn, r); err != nil {
				return fmt.Errorf("enumerate auth users: %w", err)
			}
			if err := assessExtensions(ctx, conn, r); err != nil {
				return fmt.Errorf("enumerate extensions: %w", err)
			}
			// Storage + functions live outside the Postgres schema on
			// Supabase; they need the service key + the Supabase REST
			// API. If we don't have the key, mark the sections as
			// "skipped" instead of pretending they're clean.
			if serviceKey == "" {
				r.storage = []item{{name: "(skipped — SUPABASE_SERVICE_KEY not set)", grade: gradeWarn, note: "storage enumeration requires the service-role key"}}
				r.functions = []item{{name: "(skipped — SUPABASE_SERVICE_KEY not set)", grade: gradeWarn, note: "edge-functions enumeration requires the service-role key"}}
			} else {
				// Storage / functions REST enumeration lands in a
				// follow-up commit. For the first shipped version,
				// mark them as "needs review — manual check for
				// now" so the tenant knows they exist but the CLI
				// isn't yet automating this half.
				r.storage = []item{{name: "(automation pending)", grade: gradeWarn, note: "run the Supabase dashboard's storage tab and copy the bucket list into your migration plan by hand for this run"}}
				r.functions = []item{{name: "(automation pending)", grade: gradeWarn, note: "run `supabase functions list` in your Supabase project and note each function name for #272"}}
			}

			// Hoist all blockers into the summary section.
			for _, group := range [][]item{r.tables, r.policies, r.authUsers, r.extensions, r.storage, r.functions} {
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
		       pg_total_relation_size(c.oid) AS bytes
		FROM   pg_class c
		JOIN   pg_namespace n ON n.oid = c.relnamespace
		LEFT   JOIN pg_stat_user_tables s
		         ON s.schemaname = n.nspname AND s.relname = c.relname
		WHERE  n.nspname = 'public'
		  AND  c.relkind = 'r'
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
		if err := rows.Scan(&name, &rowCount, &bytes); err != nil {
			return err
		}
		r.tables = append(r.tables, item{
			name:  fmt.Sprintf("public.%s", name),
			grade: gradeOK,
			note:  fmt.Sprintf("%s rows · %s", formatCount(rowCount), formatBytes(bytes)),
		})
	}
	return rows.Err()
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
		r.policies = append(r.policies, item{
			name:  fmt.Sprintf("%s :: %s", onTable, name),
			grade: gradeWarn,
			note:  "policy body not readable from this Postgres role — will be translated in #269",
		})
	}
	return rows.Err()
}

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
		return "policy reads app_metadata from JWT — verify translation manually (#269 will translate common shapes)", gradeWarn
	}
	if strings.Contains(lower, "auth.jwt()") && strings.Contains(lower, "user_metadata") {
		return "policy reads user_metadata from JWT — verify translation manually", gradeWarn
	}
	if strings.Contains(lower, "auth.email()") {
		return "policy uses auth.email() — Eurobase equivalent auth_email() is per-tenant, will translate in #269", gradeOK
	}
	if strings.Contains(lower, "auth.uid()") || strings.Contains(lower, "auth.role()") {
		return "standard auth.uid()/auth.role() — will translate cleanly in #269", gradeOK
	}
	// A policy body that touches neither auth.* nor storage.* is
	// likely fine but worth a look.
	return "no auth.* references — will translate as-is", gradeOK
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

func assessExtensions(ctx context.Context, conn *pgx.Conn, r *report) error {
	rows, err := conn.Query(ctx, `
		SELECT extname FROM pg_extension WHERE extname NOT IN ('plpgsql', 'plpython3u') ORDER BY extname
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	// Extensions Eurobase's tenant Postgres supports out of the box.
	// Anything outside this set is worth flagging.
	supported := map[string]bool{
		"pg_stat_statements": true,
		"pgcrypto":           true,
		"uuid-ossp":          true, // legacy; gen_random_uuid() from pgcrypto is preferred
		"pg_trgm":            true,
		"unaccent":           true,
		"btree_gin":          true,
		"btree_gist":         true,
		"citext":             true,
		"hstore":             true,
		"tablefunc":          true,
	}
	// Extensions we specifically don't support and can't migrate
	// as-is. Note about `vector`: Supabase ships pgvector as default,
	// and it's the single most common reason people can't move to a
	// backend that lacks it. We flag it as a blocker (not a warn) so
	// the report can't be misread as "vector is fine".
	blockers := map[string]string{
		"pg_graphql":     "Eurobase does not offer GraphQL over Postgres; rewrite callers to use the SDK",
		"pg_net":         "Eurobase does not expose pg_net; use an edge function to call HTTP from SQL",
		"pg_cron":        "Eurobase has its own cron surface (`eurobase cron`); reproduce schedules there",
		"pg_jsonschema":  "not available; validate JSON in application code or edge functions",
		"pgjwt":          "Eurobase issues its own JWTs; direct usage inside SQL not supported",
		"pgsodium":       "not available; use the vault or edge functions for cryptography",
		"supabase_vault": "Supabase-proprietary vault schema; use `eurobase vault` for the equivalent",
		"vector":         "pgvector-style vector similarity search not currently enabled by default — contact support before migrating a project that depends on it",
		"http":           "pg http extension not available for the same reason as pg_net",
		"plv8":           "V8 procedural language not available on Eurobase's Postgres",
		"timescaledb":    "TimescaleDB not enabled; needs support conversation before migration",
		"wrappers":       "Foreign-data-wrapper framework (used by supabase_vault etc.) not available",
	}
	// Supabase ships these; we don't lose data by migrating without
	// them, but we also don't provide equivalent functionality
	// out-of-the-box. Warn so the tenant knows to redesign around
	// the gap.
	warns := map[string]string{
		"pgaudit":    "Postgres audit logging — Eurobase's audit_log covers admin actions but does not replay pgaudit output",
		"pg_tle":     "trusted-language extensions framework — not available; move any TLE-installed extensions to app code",
		"pgmq":       "Postgres message queue — reproduce with an application queue or edge-function poller",
		"pg_repack":  "online table rewriter — not needed on Eurobase (managed vacuum), but check any dependent scripts",
		"postgis":    "PostGIS is not enabled by default — contact support if you rely on it; migration is feasible but manual",
	}
	for rows.Next() {
		var ext string
		if err := rows.Scan(&ext); err != nil {
			return err
		}
		if reason, isBlocker := blockers[ext]; isBlocker {
			r.extensions = append(r.extensions, item{name: ext, grade: gradeBlocker, note: reason})
			continue
		}
		if reason, isWarn := warns[ext]; isWarn {
			r.extensions = append(r.extensions, item{name: ext, grade: gradeWarn, note: reason})
			continue
		}
		if supported[ext] {
			note := "supported on Eurobase's Postgres"
			// Small nudge for the deprecated uuid-ossp on top of the
			// support note.
			if ext == "uuid-ossp" {
				note = "supported, but gen_random_uuid() from pgcrypto is preferred over uuid-ossp for new columns"
			}
			r.extensions = append(r.extensions, item{name: ext, grade: gradeOK, note: note})
			continue
		}
		r.extensions = append(r.extensions, item{
			name: ext, grade: gradeWarn,
			note: "not in Eurobase's default-supported list — check with support before relying on it post-migration",
		})
	}
	return rows.Err()
}

// ── report writer ────────────────────────────────────────────────────

func writeReport(w io.Writer, r *report) error {
	buf := &strings.Builder{}
	fmt.Fprintf(buf, "# Supabase → Eurobase migration report\n\n")
	fmt.Fprintf(buf, "> Source: `%s`\n", r.sourceURL)
	fmt.Fprintf(buf, "> Generated: %s UTC\n\n", r.generatedAt.Format(time.RFC3339))

	if len(r.blockers) > 0 {
		fmt.Fprintf(buf, "## %s Blockers — %d found\n\n", gradeBlocker, len(r.blockers))
		fmt.Fprintf(buf, "Address these before running the migration steps. They are things Eurobase can't do today.\n\n")
		for _, it := range r.blockers {
			fmt.Fprintf(buf, "- **%s** — %s\n", mdEscape(it.name), mdEscape(it.note))
		}
		fmt.Fprintf(buf, "\n")
	} else {
		fmt.Fprintf(buf, "## %s No blockers — this project looks migratable end-to-end.\n\n", gradeOK)
	}

	writeSection(buf, "Tables", r.tables)
	writeSection(buf, "RLS policies", r.policies)
	writeSection(buf, "Auth users + providers", r.authUsers)
	writeSection(buf, "Storage", r.storage)
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
		fmt.Fprintf(buf, "- %s **%s** — %s\n", it.grade, mdEscape(it.name), mdEscape(it.note))
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
		"42883",   // undefined_function (e.g. pg_get_expr signature diff)
		"42P01":   // undefined_table
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
		"42P01",   // undefined_table
		"42501":   // insufficient_privilege (safe to treat as "missing" for reporting purposes)
		return true
	}
	return false
}
