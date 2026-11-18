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

// sbFunction is a database function in the project's public schema.
type sbFunction struct {
	Name   string
	SecDef bool
	Body   string // prosrc; used for usage detection only
}

// sbTrigger is a user trigger on a table in public, auth or storage.
type sbTrigger struct {
	TableSchema, Table, Name string
	FuncSchema, FuncName     string
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
	vaultSecrets  int // -1 when unknown (vault not installed or not readable)
	realtimeAll   bool
	realtimeTabs  []string
}

// ── enumerators ──────────────────────────────────────────────────────

func readSupabaseCode(ctx context.Context, conn *pgx.Conn) (*sbCode, error) {
	c := &sbCode{vaultSecrets: -1}

	// Functions in public, excluding ones that belong to an extension.
	rows, err := conn.Query(ctx, `
		SELECT p.proname, p.prosecdef, COALESCE(p.prosrc, '')
		FROM   pg_proc p
		JOIN   pg_namespace n ON n.oid = p.pronamespace
		WHERE  n.nspname = 'public'
		  AND  p.prokind IN ('f', 'p')
		  AND  NOT EXISTS (SELECT 1 FROM pg_depend d
		                   WHERE d.classid = 'pg_proc'::regclass
		                     AND d.objid = p.oid AND d.deptype = 'e')
		ORDER BY p.proname`)
	if err != nil {
		return nil, fmt.Errorf("list functions: %w", err)
	}
	for rows.Next() {
		var f sbFunction
		if err := rows.Scan(&f.Name, &f.SecDef, &f.Body); err != nil {
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
		SELECT n.nspname, c.relname, t.tgname, pn.nspname, p.proname
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
		if err := rows.Scan(&t.TableSchema, &t.Table, &t.Name, &t.FuncSchema, &t.FuncName); err != nil {
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
		         WHERE n.nspname = 'public' AND t.typtype = 'e'),
		       (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		         WHERE n.nspname = 'public' AND c.relkind = 'S')`).Scan(&c.enumCount, &c.sequenceCount); err != nil {
		return nil, fmt.Errorf("count types: %w", err)
	}

	// pg_cron jobs, only when the extension is installed.
	var hasCron bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron')`).Scan(&hasCron); err != nil {
		return nil, err
	}
	if hasCron {
		c.cronJobs = []string{}
		rows, err := conn.Query(ctx, `SELECT COALESCE(jobname, jobid::text), command FROM cron.job ORDER BY 1`)
		if err != nil && !isMissingObjectError(err) {
			return nil, fmt.Errorf("list cron jobs: %w", err)
		}
		if err == nil {
			for rows.Next() {
				var name, cmd string
				if err := rows.Scan(&name, &cmd); err != nil {
					rows.Close()
					return nil, err
				}
				c.cronJobs = append(c.cronJobs, name)
				c.cronCommands = append(c.cronCommands, cmd)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
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
		where := "function " + f.Name
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
		if !f.SecDef {
			invoker = append(invoker, f.Name)
			continue
		}
		out = append(out, item{
			name:  "function: public." + f.Name,
			grade: gradeBlocker,
			note:  "SECURITY DEFINER: it runs with its owner's rights and skips RLS, so Eurobase doesn't accept it. Rewrite it as SECURITY INVOKER with RLS policies that allow what it needs, or move the work into server code that uses your secret key, or into an edge function",
		})
	}
	if len(invoker) > 0 {
		out = append(out, item{
			name:  fmt.Sprintf("%d function(s) (SECURITY INVOKER)", len(invoker)),
			grade: gradeOK,
			note:  "copied by the `schema` step: " + nameList(invoker),
		})
	}
	return out
}

func gradeTriggers(trs []sbTrigger) []item {
	var out []item
	var own []string
	for _, t := range trs {
		on := t.TableSchema + "." + t.Table
		// Supabase's own triggers on its auth / storage tables.
		if (t.TableSchema == "auth" || t.TableSchema == "storage") && t.FuncSchema == t.TableSchema {
			continue
		}
		switch {
		case isSupabaseWebhook(t):
			event := "db.insert / db.update / db.delete"
			if t.TableSchema == "auth" && t.Table == "users" {
				event = "auth.signup"
			}
			out = append(out, item{
				name:  fmt.Sprintf("database webhook: %s on %s", t.Name, on),
				grade: gradeBlocker,
				note:  fmt.Sprintf("Supabase database webhooks (pg_net) aren't migrated. Create a Eurobase webhook or function trigger on %s instead. Eurobase sends these events for writes made through its API; writes made with SQL don't send them", event),
			})
		case t.TableSchema == "auth":
			note := fmt.Sprintf("a trigger on Supabase's %s isn't migrated: Eurobase's user tables are platform-managed and don't accept triggers. ", on)
			if t.Table == "users" {
				note += "For work on signup (e.g. creating a profile row), use a function trigger or webhook on auth.signup, or do it in your signup code"
			} else {
				note += "Move this logic into your app, an edge function, or a function trigger on an auth event"
			}
			out = append(out, item{
				name:  fmt.Sprintf("trigger: %s on %s → %s.%s()", t.Name, on, t.FuncSchema, t.FuncName),
				grade: gradeBlocker,
				note:  note,
			})
		case t.TableSchema == "storage":
			out = append(out, item{
				name:  fmt.Sprintf("trigger: %s on %s → %s.%s()", t.Name, on, t.FuncSchema, t.FuncName),
				grade: gradeWarn,
				note:  "a trigger on Supabase's storage tables isn't migrated (Eurobase's file table is platform-managed). Use a webhook or function trigger on storage.upload / storage.delete",
			})
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
			note := "this view runs with its owner's rights and skips RLS on the tables it reads, so Eurobase refuses it. Run `ALTER VIEW public." + ident(v.Name) + " SET (security_invoker = true)` and check that the tables' policies allow the reads"
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

// ident makes a database identifier safe to put inside a note, where
// backticks mark code spans.
func ident(s string) string {
	return strings.ReplaceAll(s, "`", "")
}
