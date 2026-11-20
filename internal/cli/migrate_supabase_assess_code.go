package cli

// Checks for the parts of a Supabase project that live in SQL rather
// than in tables: database functions, triggers (including Supabase's
// database webhooks), views, realtime, and which default-installed
// extensions are actually used. These are where a migration needs real
// rewrites, so `assess` must name them.
//
// Each check is an enumerator (one read-only query) plus a pure grading
// function, so the grading is unit-tested without a database. Function
// and trigger bodies are read only to detect usage; they never appear in
// the report (they can hold secrets, e.g. a webhook URL).

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// sbFunction is a database function the project defined: in public or
// in another schema of its own (e.g. a `private` schema for helpers).
type sbFunction struct {
	Schema string
	Name   string
	SecDef bool
	Body   string // prosrc; used for usage detection only
}

// sbTrigger is a user trigger on a table in public, auth or storage.
type sbTrigger struct {
	TableSchema, Table, Name string
	FuncSchema, FuncName     string
	Events                   []string // INSERT / UPDATE / DELETE / TRUNCATE
}

// supabaseSchemas are schemas Supabase (or Postgres) manages. Functions
// in them aren't the project's own; everything else besides public is a
// schema the developer created, which the `schema` step doesn't copy.
var supabaseSchemas = []string{
	"pg_catalog", "information_schema", "pg_toast", "auth", "storage",
	"realtime", "_realtime", "extensions", "graphql", "graphql_public",
	"vault", "net", "pgbouncer", "supabase_functions", "supabase_migrations",
	"cron", "pgsodium", "pgsodium_masks", "_analytics", "pgtle", "topology",
}

// sbView is a view or materialized view in public.
type sbView struct {
	Name            string
	Materialized    bool
	SecurityInvoker bool
	AnonCanRead     bool
}

// sbCode is everything the code checks read, gathered before grading so
// the extension grading can say whether an extension is actually used.
type sbCode struct {
	functions     []sbFunction
	triggers      []sbTrigger
	views         []sbView
	enumCount     int
	sequenceCount int
	cronJobs      []string // job names; nil when pg_cron isn't installed
	cronCommands  []string
	cronUnknown   bool // pg_cron installed but cron.job not readable
	vaultSecrets  int  // -1 when unknown (vault not installed or not readable)
	realtimeAll   bool
	realtimeTabs  []string
}

// ── enumerators ──────────────────────────────────────────────────────

func readSupabaseCode(ctx context.Context, conn *pgx.Conn) (*sbCode, error) {
	c := &sbCode{vaultSecrets: -1}

	// The project's own functions (public and its own schemas),
	// excluding ones that belong to an extension.
	rows, err := conn.Query(ctx, `
		SELECT n.nspname, p.proname, p.prosecdef, COALESCE(p.prosrc, '')
		FROM   pg_proc p
		JOIN   pg_namespace n ON n.oid = p.pronamespace
		WHERE  n.nspname <> ALL ($1::text[])
		  AND  n.nspname NOT LIKE 'pg\_temp\_%' AND n.nspname NOT LIKE 'pg\_toast\_temp\_%'
		  AND  p.prokind IN ('f', 'p')
		  AND  NOT EXISTS (SELECT 1 FROM pg_depend d
		                   WHERE d.classid = 'pg_proc'::regclass
		                     AND d.objid = p.oid AND d.deptype = 'e')
		ORDER BY n.nspname = 'public' DESC, n.nspname, p.proname`, supabaseSchemas)
	if err != nil {
		return nil, fmt.Errorf("list functions: %w", err)
	}
	for rows.Next() {
		var f sbFunction
		if err := rows.Scan(&f.Schema, &f.Name, &f.SecDef, &f.Body); err != nil {
			rows.Close()
			return nil, err
		}
		c.functions = append(c.functions, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = conn.Query(ctx, `
		SELECT n.nspname, c.relname, t.tgname, pn.nspname, p.proname,
		       ARRAY_REMOVE(ARRAY[
		         CASE WHEN t.tgtype & 4  <> 0 THEN 'INSERT' END,
		         CASE WHEN t.tgtype & 16 <> 0 THEN 'UPDATE' END,
		         CASE WHEN t.tgtype & 8  <> 0 THEN 'DELETE' END,
		         CASE WHEN t.tgtype & 32 <> 0 THEN 'TRUNCATE' END], NULL)
		FROM   pg_trigger t
		JOIN   pg_class c      ON c.oid = t.tgrelid
		JOIN   pg_namespace n  ON n.oid = c.relnamespace
		JOIN   pg_proc p       ON p.oid = t.tgfoid
		JOIN   pg_namespace pn ON pn.oid = p.pronamespace
		WHERE  NOT t.tgisinternal
		  AND  n.nspname IN ('public', 'auth', 'storage')
		ORDER BY 1, 2, 3`)
	if err != nil {
		return nil, fmt.Errorf("list triggers: %w", err)
	}
	for rows.Next() {
		var t sbTrigger
		if err := rows.Scan(&t.TableSchema, &t.Table, &t.Name, &t.FuncSchema, &t.FuncName, &t.Events); err != nil {
			rows.Close()
			return nil, err
		}
		c.triggers = append(c.triggers, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Views. AnonCanRead is checked only where Supabase's anon role
	// exists (it does on every Supabase project).
	rows, err = conn.Query(ctx, `
		SELECT c.relname,
		       c.relkind = 'm',
		       COALESCE(array_to_string(c.reloptions, ','), ''),
		       CASE WHEN EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon')
		            THEN has_table_privilege('anon', c.oid, 'SELECT')
		            ELSE false END
		FROM   pg_class c
		JOIN   pg_namespace n ON n.oid = c.relnamespace
		WHERE  n.nspname = 'public' AND c.relkind IN ('v', 'm')
		  AND  NOT EXISTS (SELECT 1 FROM pg_depend d
		                   WHERE d.classid = 'pg_class'::regclass
		                     AND d.objid = c.oid AND d.deptype = 'e')
		ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	for rows.Next() {
		var v sbView
		var opts string
		if err := rows.Scan(&v.Name, &v.Materialized, &opts, &v.AnonCanRead); err != nil {
			rows.Close()
			return nil, err
		}
		v.SecurityInvoker = hasSecurityInvoker(opts)
		c.views = append(c.views, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := conn.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
		         WHERE n.nspname = 'public' AND t.typtype = 'e'
		           AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass
		                           AND d.objid = t.oid AND d.deptype = 'e')),
		       (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		         WHERE n.nspname = 'public' AND c.relkind = 'S'
		           AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass
		                           AND d.objid = c.oid AND d.deptype = 'e'))`).Scan(&c.enumCount, &c.sequenceCount); err != nil {
		return nil, fmt.Errorf("count types: %w", err)
	}

	// pg_cron jobs, only when the extension is installed.
	var hasCron bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron')`).Scan(&hasCron); err != nil {
		return nil, err
	}
	if hasCron {
		names, cmds, err := readCronJobs(ctx, conn)
		switch {
		case err == nil:
			c.cronJobs, c.cronCommands = names, cmds
		case isMissingObjectError(err):
			c.cronUnknown = true
		default:
			return nil, fmt.Errorf("list cron jobs: %w", err)
		}
	}

	// Supabase Vault secrets (count only).
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM vault.secrets`).Scan(&n); err == nil {
		c.vaultSecrets = n
	} else if !isMissingObjectError(err) {
		return nil, fmt.Errorf("count vault secrets: %w", err)
	}

	// Realtime: tables in Supabase's publication.
	var pubAll *bool
	if err := conn.QueryRow(ctx, `SELECT puballtables FROM pg_publication WHERE pubname = 'supabase_realtime'`).Scan(&pubAll); err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("read realtime publication: %w", err)
	}
	if pubAll != nil && *pubAll {
		c.realtimeAll = true
	} else if pubAll != nil {
		rows, err := conn.Query(ctx, `
			SELECT schemaname || '.' || tablename FROM pg_publication_tables
			WHERE pubname = 'supabase_realtime' ORDER BY 1`)
		if err != nil {
			return nil, fmt.Errorf("list realtime tables: %w", err)
		}
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				rows.Close()
				return nil, err
			}
			c.realtimeTabs = append(c.realtimeTabs, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// readCronJobs reads pg_cron's jobs. A permission error can surface from
// Query or, in pgx v5, only from rows.Err(); both are returned as-is so
// the caller can degrade on them.
func readCronJobs(ctx context.Context, conn *pgx.Conn) (names, cmds []string, err error) {
	rows, err := conn.Query(ctx, `SELECT COALESCE(jobname, jobid::text), command FROM cron.job ORDER BY 1`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	names, cmds = []string{}, []string{}
	for rows.Next() {
		var n, c string
		if err := rows.Scan(&n, &c); err != nil {
			return nil, nil, err
		}
		names = append(names, n)
		cmds = append(cmds, c)
	}
	return names, cmds, rows.Err()
}

// hasSecurityInvoker reports whether a view's reloptions (comma-joined)
// turn security_invoker on.
func hasSecurityInvoker(opts string) bool {
	for _, o := range strings.Split(opts, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(o), "=")
		if ok && strings.EqualFold(k, "security_invoker") {
			switch strings.ToLower(v) {
			case "true", "on", "1", "yes":
				return true
			}
		}
	}
	return false
}

// ── usage detection ──────────────────────────────────────────────────

var (
	pgNetUseRe    = regexp.MustCompile(`(?i)\bnet\s*\.\s*_?http_`)
	vaultUseRe    = regexp.MustCompile(`(?i)\bvault\s*\.\s*(decrypted_secrets|secrets|create_secret|update_secret)\b`)
	pgsodiumUseRe = regexp.MustCompile(`(?i)\bpgsodium\s*\.`)
)

// isSupabaseWebhook reports whether a trigger is one of Supabase's
// database webhooks (they call supabase_functions.http_request, which
// is built on pg_net).
func isSupabaseWebhook(t sbTrigger) bool {
	return t.FuncSchema == "supabase_functions" && t.FuncName == "http_request"
}

// extensionUse lists where pg_net / Supabase Vault / pgsodium are used:
// function bodies, database webhooks, cron commands.
func (c *sbCode) extensionUse() (pgNet, vault, pgsodium []string) {
	for _, f := range c.functions {
		where := "function " + f.Schema + "." + f.Name
		if pgNetUseRe.MatchString(f.Body) {
			pgNet = append(pgNet, where)
		}
		if vaultUseRe.MatchString(f.Body) {
			vault = append(vault, where)
		}
		if pgsodiumUseRe.MatchString(f.Body) {
			pgsodium = append(pgsodium, where)
		}
	}
	for _, t := range c.triggers {
		if isSupabaseWebhook(t) {
			pgNet = append(pgNet, fmt.Sprintf("database webhook %s on %s.%s", t.Name, t.TableSchema, t.Table))
		}
	}
	for i, cmd := range c.cronCommands {
		where := "cron job " + c.cronJobs[i]
		if pgNetUseRe.MatchString(cmd) {
			pgNet = append(pgNet, where)
		}
		if vaultUseRe.MatchString(cmd) {
			vault = append(vault, where)
		}
	}
	return pgNet, vault, pgsodium
}

// ── grading ──────────────────────────────────────────────────────────

func gradeFunctions(fns []sbFunction) []item {
	var out []item
	var invoker []string
	for _, f := range fns {
		qualified := f.Schema + "." + f.Name
		switch {
		case f.SecDef:
			note := "SECURITY DEFINER: it runs with its owner's rights and skips RLS. The `schema` step converts it into a normal (SECURITY INVOKER) function that runs with the caller's rights, as one of the access changes you accept. Review what it needs the owner's rights for: grant that with RLS policies, move the work into server code that uses your secret key or into an edge function, or close the function to app users with an is_service_role() check"
			if f.Schema != "public" {
				note += fmt.Sprintf(". It is also in schema %s, which the `schema` step doesn't copy; policies or triggers that call it fail on import", ident(f.Schema))
			}
			out = append(out, item{name: "function: " + qualified, grade: gradeBlocker, note: note})
		case f.Schema != "public":
			out = append(out, item{
				name:  "function: " + qualified,
				grade: gradeWarn,
				note:  fmt.Sprintf("schema %s isn't copied by the `schema` step (it copies public and storage). Recreate this function in public and update the policies, triggers and calls that use it", ident(f.Schema)),
			})
		default:
			invoker = append(invoker, f.Name)
		}
	}
	if len(invoker) > 0 {
		out = append(out, item{
			name:  fmt.Sprintf("%d function(s) in public (SECURITY INVOKER)", len(invoker)),
			grade: gradeOK,
			note:  "copied by the `schema` step: " + nameList(invoker),
		})
	}
	return out
}

// webhookEvents maps the operations of a Supabase database webhook on a
// table to the Eurobase webhook events that replace it, and lists the
// operations with no Eurobase event.
func webhookEvents(t sbTrigger) (eurobase, missing []string) {
	add := func(e string) {
		for _, x := range eurobase {
			if x == e {
				return
			}
		}
		eurobase = append(eurobase, e)
	}
	for _, op := range t.Events {
		switch {
		case t.TableSchema == "auth" && t.Table == "users":
			switch op {
			case "INSERT":
				add("auth.user.created")
			case "DELETE":
				add("auth.user.deleted")
			default:
				missing = append(missing, op)
			}
		case t.TableSchema == "storage" && t.Table == "objects":
			switch op {
			case "INSERT", "UPDATE":
				add("storage.upload")
			case "DELETE":
				add("storage.delete")
			default:
				missing = append(missing, op)
			}
		case t.TableSchema == "auth" || t.TableSchema == "storage":
			missing = append(missing, op)
		default:
			switch op {
			case "INSERT":
				add("db.insert")
			case "UPDATE":
				add("db.update")
			case "DELETE":
				add("db.delete")
			default:
				missing = append(missing, op)
			}
		}
	}
	return eurobase, missing
}

func gradeTriggers(trs []sbTrigger) []item {
	var out []item
	var own []string
	for _, t := range trs {
		on := ident(t.TableSchema + "." + t.Table)
		fn := t.FuncSchema + "." + t.FuncName
		// Supabase's own triggers on its auth / storage tables.
		if (t.TableSchema == "auth" || t.TableSchema == "storage") && t.FuncSchema == t.TableSchema {
			continue
		}
		switch {
		case isSupabaseWebhook(t):
			evs, missing := webhookEvents(t)
			note := "Supabase database webhooks (pg_net) aren't migrated. "
			if len(evs) > 0 {
				note += "Create a Eurobase webhook on " + strings.Join(evs, " / ") + " instead (Eurobase sends these for changes made through its API, not for writes made with SQL)"
			}
			if len(missing) > 0 {
				if len(evs) > 0 {
					note += ". "
				}
				note += "Eurobase has no event for " + strings.ToLower(strings.Join(missing, " / ")) + " on this table; send that notification from your app or an edge function"
			}
			out = append(out, item{name: fmt.Sprintf("database webhook: %s on %s", t.Name, on), grade: gradeBlocker, note: note})
		case t.TableSchema == "auth":
			note := fmt.Sprintf("a trigger on Supabase's %s isn't migrated: Eurobase's user tables are platform-managed and don't accept triggers. ", on)
			if t.Table == "users" {
				note += "For work on signup (e.g. creating a profile row), do it in your signup code, or have a webhook on auth.user.created call your backend"
			} else {
				note += "Move this logic into your app or an edge function"
			}
			out = append(out, item{name: fmt.Sprintf("trigger: %s on %s → %s()", t.Name, on, fn), grade: gradeBlocker, note: note})
		case t.TableSchema == "storage":
			out = append(out, item{
				name:  fmt.Sprintf("trigger: %s on %s → %s()", t.Name, on, fn),
				grade: gradeWarn,
				note:  "a trigger on Supabase's storage tables isn't migrated (Eurobase's file table is platform-managed). Use a webhook on storage.upload / storage.delete",
			})
		case t.FuncSchema != "public":
			note := fmt.Sprintf("its function is in schema %s, which the `schema` step doesn't copy, so creating this trigger fails on import. Recreate the function in public", ident(t.FuncSchema))
			if t.FuncName == "moddatetime" {
				note += " (for moddatetime: a plpgsql trigger function that sets the column to now())"
			}
			out = append(out, item{name: fmt.Sprintf("trigger: %s on %s → %s()", t.Name, on, fn), grade: gradeBlocker, note: note})
		default:
			own = append(own, t.Name+" on "+t.Table)
		}
	}
	if len(own) > 0 {
		out = append(out, item{
			name:  fmt.Sprintf("%d trigger(s) on your tables", len(own)),
			grade: gradeOK,
			note:  "copied by the `schema` step: " + nameList(own),
		})
	}
	return out
}

func gradeViews(views []sbView, enums, sequences int) []item {
	var out []item
	for _, v := range views {
		switch {
		case v.Materialized:
			out = append(out, item{
				name:  "materialized view: public." + v.Name,
				grade: gradeBlocker,
				note:  "Eurobase doesn't accept materialized views (they're read with the owner's rights, skipping RLS). Use a table you refresh from a cron job, or a view with security_invoker",
			})
		case !v.SecurityInvoker:
			note := "this view runs with its owner's rights and skips RLS on the tables it reads, so Eurobase refuses it. "
			if q, ok := sqlName("public", v.Name); ok {
				note += "Run `ALTER VIEW " + q + " SET (security_invoker = true)` and check"
			} else {
				note += "Set security_invoker = true on it and check"
			}
			note += " that the tables' policies allow the reads"
			if v.AnonCanRead {
				note += ". On Supabase today, anyone with your anon key can read all of its rows"
			}
			out = append(out, item{name: "view: public." + v.Name, grade: gradeBlocker, note: note})
		default:
			out = append(out, item{name: "view: public." + v.Name, grade: gradeOK, note: "security_invoker view, copied by the `schema` step"})
		}
	}
	if enums > 0 || sequences > 0 {
		out = append(out, item{
			name:  fmt.Sprintf("%d enum type(s), %d sequence(s)", enums, sequences),
			grade: gradeOK,
			note:  "copied by the `schema` step",
		})
	}
	return out
}

func gradeRealtime(all bool, tables []string) []item {
	note := "switch subscriptions to the Eurobase SDK's realtime. Eurobase sends realtime events for writes made through its API; writes made with SQL (functions, triggers, cron) don't send events"
	switch {
	case all:
		return []item{{name: "realtime on all tables", grade: gradeWarn, note: note}}
	case len(tables) > 0:
		return []item{{name: fmt.Sprintf("realtime on %d table(s)", len(tables)), grade: gradeWarn, note: note + ". Tables: " + nameList(tables)}}
	}
	return nil
}

// nameList renders identifiers for a note: backticks stripped (notes may
// contain code spans), at most 15 names.
func nameList(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	const max = 15
	extra := 0
	if len(sorted) > max {
		extra = len(sorted) - max
		sorted = sorted[:max]
	}
	for i, n := range sorted {
		sorted[i] = ident(n)
	}
	s := strings.Join(sorted, ", ")
	if extra > 0 {
		s += fmt.Sprintf(" and %d more", extra)
	}
	return s
}

var plainIdentRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// sqlName renders schema.name for a suggested SQL command, quoting a part
// only when Postgres would otherwise fold or misread it. ok is false when
// a part contains a backtick (it can't go inside a code span intact).
func sqlName(schema, name string) (string, bool) {
	if strings.Contains(schema+name, "`") {
		return "", false
	}
	q := func(p string) string {
		if plainIdentRe.MatchString(p) {
			return p
		}
		return pgx.Identifier{p}.Sanitize()
	}
	return q(schema) + "." + q(name), true
}

// ident makes a database identifier safe to put inside a note, where
// backticks mark code spans.
func ident(s string) string {
	return strings.ReplaceAll(s, "`", "")
}
