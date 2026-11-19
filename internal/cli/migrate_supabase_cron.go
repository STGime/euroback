package cli

// `eurobase import supabase cron` — carry a Supabase project's scheduled
// jobs (the pg_cron extension, cron.job) over to Eurobase cron jobs.
//
// A pg_cron job is a schedule and a SQL command. Two kinds come over:
//
//   - a command that is nothing but a call to one of the project's edge
//     functions (SELECT net.http_post('https://<ref>.supabase.co/
//     functions/v1/<name>', …)) becomes a job that runs the function
//     <name>, with the JSON body as its payload
//   - any other single SELECT / INSERT / UPDATE / DELETE / CALL statement
//     becomes a SQL job, translated the way the schema import translates
//     SQL (no "public." prefix, …)
//
// The rule for everything else: a job is only planned when it would do
// what the pg_cron job does, at the same times. Whatever can't be said
// that way is listed with the reason and not created — a job that runs
// on other days, or that quietly leaves out half of the command, is
// worse than a job the developer knows is missing.
//
// The command reads the source and prints the plan. With --apply it
// creates the jobs in the active project — switched off, because the
// same jobs are still running on Supabase until the switch-over.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/cron"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
)

func importSupabaseCronCmd() *cobra.Command {
	var (
		apply    bool
		enable   bool
		timezone string
	)
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Carry a Supabase project's scheduled jobs (pg_cron) over to Eurobase cron jobs",
		Long: `Read the scheduled jobs of a Supabase project (pg_cron's cron.job) and
show how each becomes a Eurobase cron job. With --apply, create them in
the active project.

  SELECT / INSERT / UPDATE / DELETE / CALL  → a SQL job, same statement
  SELECT net.http_post('…/functions/v1/name', …)
                                            → a job that runs the function "name"

Schedules are rewritten where the two differ (a range such as 1-5 becomes
1,2,3,4,5; MON becomes 1). The time zone is the source's cron.timezone
(UTC unless you changed it); --timezone sets another.

A job is only planned when it would do the same thing at the same times.
Anything else is listed with the reason: several statements, a call
wrapped in other SQL, a URL that isn't one of your functions, SQL that
uses Supabase's own schemas, maintenance statements, a schedule in
seconds.

Jobs are created switched off: the same jobs keep running on Supabase
until you switch over, and most must not run twice. Switch them on in the
console (Cron) when you do, or pass --enable to create them as they are
on Supabase. Request headers are never carried over (they usually hold a
key); a SQL job's statement may take up to 30 seconds.

Needs SUPABASE_DB_URL. The source is only read.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			dbURL := strings.TrimSpace(os.Getenv("SUPABASE_DB_URL"))
			if dbURL == "" {
				return errors.New("SUPABASE_DB_URL is not set")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			conn, err := pgx.Connect(ctx, dbURL)
			if err != nil {
				return fmt.Errorf("connect to Supabase: %w", err)
			}
			defer conn.Close(ctx)
			tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
			if err != nil {
				return fmt.Errorf("start read-only transaction: %w", err)
			}
			defer tx.Rollback(ctx) //nolint:errcheck

			jobs, sourceZone, err := readPgCronJobs(ctx, tx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(jobs) == 0 {
				fmt.Fprintln(out, "No scheduled jobs: the source has no pg_cron jobs (cron.job is empty or the extension isn't installed).")
				return nil
			}
			if timezone == "" {
				timezone = sourceZone
			}
			// The scheduler loads the zone by its IANA name; pg_cron also
			// takes forms it can't ("+02"). Better found now than as a
			// refusal for every job.
			if err := checkCronTimezone(timezone); err != nil {
				return err
			}

			plans := planCronJobs(jobs, timezone)
			ready := 0
			for _, p := range plans {
				if p.Ready() {
					ready++
				}
				printCronPlan(out, p)
			}
			fmt.Fprintf(out, "\n%d job(s): %d ready, %d need changes\n", len(plans), ready, len(plans)-ready)

			if !apply {
				if ready > 0 {
					fmt.Fprintln(out, "\nNext: run again with --apply to create the ready jobs in the active project (switched off).")
				}
				return nil
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
			functions := deployedFunctionNames(client, cfg.ActiveProject)
			fmt.Fprintf(out, "\nCreating in %s\n", ProjectLabel(cfg))
			failed := 0
			for _, p := range plans {
				if !p.Ready() {
					continue
				}
				req := p.Request
				on := enable && p.Source.Active
				req.Enabled = &on
				if _, err := client.Post("/platform/projects/"+cfg.ActiveProject+"/cron", req); err != nil {
					if strings.Contains(err.Error(), "already exists") {
						fmt.Fprintf(out, "  %-36s – a job with this name is already there; left as it is\n", req.Name)
						continue
					}
					failed++
					fmt.Fprintf(out, "  %-36s ✗ %v\n", req.Name, err)
					if strings.Contains(err.Error(), "free plan") {
						// The same answer would come for every job left.
						return errors.New("the project's plan allows no more scheduled jobs; the remaining jobs were not created")
					}
					continue
				}
				state := "switched off"
				if on {
					state = "switched on"
				}
				note := ""
				if req.ActionType == "function" && functions != nil && !functions[req.Action] {
					note = fmt.Sprintf(" — the function %q isn't deployed in this project yet", req.Action)
				}
				fmt.Fprintf(out, "  %-36s ✓ created, %s%s\n", req.Name, state, note)
			}
			if failed > 0 {
				return fmt.Errorf("%d job(s) could not be created", failed)
			}
			if !enable {
				fmt.Fprintln(out, "\nThe jobs are switched off. Switch each one on in the console (Cron) when you move over from Supabase.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Create the ready jobs in the active project")
	cmd.Flags().BoolVar(&enable, "enable", false, "With --apply: create jobs switched on where they are active on Supabase (default: all switched off)")
	cmd.Flags().StringVar(&timezone, "timezone", "", "IANA time zone for the jobs (default: the source's cron.timezone)")
	return cmd
}

var ianaZoneRe = regexp.MustCompile(`^(UTC|[A-Z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)+)$`)

// checkCronTimezone reports whether the scheduler can use the zone: an
// IANA name as it is written (the server's files are case-sensitive).
func checkCronTimezone(tz string) error {
	if _, err := time.LoadLocation(tz); err != nil || !ianaZoneRe.MatchString(tz) {
		return fmt.Errorf("the time zone is %q, which a Eurobase cron job can't use — run again with --timezone and an IANA name such as Europe/Berlin", tz)
	}
	return nil
}

// deployedFunctionNames lists the project's edge functions, or nil when
// they can't be listed (the note about a missing function is then left
// out; the job is created either way).
func deployedFunctionNames(client *APIClient, projectID string) map[string]bool {
	data, err := client.Get("/platform/projects/" + projectID + "/functions")
	if err != nil {
		return nil
	}
	var fns []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &fns); err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, f := range fns {
		out[f.Name] = true
	}
	return out
}

func printCronPlan(out interface{ Write([]byte) (int, error) }, p *cronPlan) {
	if !p.Ready() {
		fmt.Fprintf(out, "  %-36s ✗ needs changes\n", p.Request.Name)
		fmt.Fprintf(out, "      on Supabase: %s  %s\n", p.Source.Schedule, shortLine(withoutKeys(p.Source.Command), 110))
		for _, r := range p.Problems {
			fmt.Fprintf(out, "      %s\n", r)
		}
		return
	}
	what := "SQL: " + shortLine(withoutKeys(p.Request.Action), 90)
	if p.Request.ActionType == "function" {
		what = "runs the function " + p.Request.Action
		if p.FunctionURL != "" {
			what += " (was " + p.FunctionURL + ")"
		}
		// The payload's values may be anything; its keys say enough.
		if keys := payloadKeys(p.Request.Payload); keys != "" {
			what += ", payload: " + keys
		}
	}
	state := ""
	if !p.Source.Active {
		state = "  (inactive on Supabase)"
	}
	fmt.Fprintf(out, "  %-36s ✓ ready  %s %s%s\n", p.Request.Name, p.Request.Schedule, p.Request.Timezone, state)
	fmt.Fprintf(out, "      %s\n", what)
	for _, n := range p.Notes {
		fmt.Fprintf(out, "      note: %s\n", n)
	}
}

// payloadKeys describes a payload without printing its values.
func payloadKeys(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return fmt.Sprintf("%d bytes of JSON", len(payload))
	}
	if len(obj) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "{" + shortLine(strings.Join(keys, ", "), 60) + "}"
}

var (
	bearerRe   = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	jwtRe      = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9._-]{8,}`)
	urlQueryRe = regexp.MustCompile(`(https?://[^\s'"?]+)\?[^\s'"]*`)
	sbKeyRe    = regexp.MustCompile(`\b(sb_(secret|publishable)_|sbp_|sk_live_|sk_test_)[A-Za-z0-9_-]{6,}`)
)

// withoutKeys hides what looks like a key in text that is about to be
// printed: pg_cron commands carry them in headers and URLs.
func withoutKeys(s string) string {
	s = bearerRe.ReplaceAllString(s, "${1}…")
	s = jwtRe.ReplaceAllString(s, "…")
	s = sbKeyRe.ReplaceAllString(s, "…")
	return urlQueryRe.ReplaceAllString(s, "${1}?…")
}

// PlanSupabaseCronJob returns the body of the request that creates the
// Eurobase job for one pg_cron job, or why there can't be one. For tests
// outside this package that send such a request to a real server.
func PlanSupabaseCronJob(id int64, name, schedule, command, timezone string) (body []byte, problems []string) {
	p := planCronJob(pgCronJob{ID: id, Name: name, Schedule: schedule, Command: command, Active: true}, timezone)
	if !p.Ready() {
		return nil, p.Problems
	}
	off := false
	p.Request.Enabled = &off
	body, _ = json.Marshal(p.Request)
	return body, nil
}

func shortLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// ── reading the source ──────────────────────────────────────────────

// pgCronJob is one row of cron.job.
type pgCronJob struct {
	ID       int64  `json:"jobid"`
	Name     string `json:"jobname"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Active   bool   `json:"active"`
}

// readPgCronJobs returns the source's jobs and the time zone pg_cron
// runs them in. No pg_cron is no jobs, not an error.
func readPgCronJobs(ctx context.Context, q sourceQuerier) ([]pgCronJob, string, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('cron.job') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, "", fmt.Errorf("look for pg_cron: %w", err)
	}
	if !exists {
		return nil, "", nil
	}
	// Whole rows as JSON: older pg_cron has no jobname column.
	rows, err := q.Query(ctx, `SELECT to_jsonb(j)::text FROM cron.job j ORDER BY j.jobid`)
	if err != nil {
		return nil, "", fmt.Errorf("read cron.job: %w", err)
	}
	defer rows.Close()
	var jobs []pgCronJob
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, "", err
		}
		j := pgCronJob{Active: true}
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, "", fmt.Errorf("read cron.job row: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var tz *string
	if err := q.QueryRow(ctx, `SELECT current_setting('cron.timezone', true)`).Scan(&tz); err != nil {
		return nil, "", fmt.Errorf("read cron.timezone: %w", err)
	}
	timezone := "UTC"
	if tz != nil && *tz != "" && !strings.EqualFold(*tz, "GMT") {
		timezone = *tz
	}
	return jobs, timezone, nil
}

// ── planning ────────────────────────────────────────────────────────

// cronJobRequest is the body of POST …/cron.
type cronJobRequest struct {
	Name        string          `json:"name"`
	Schedule    string          `json:"schedule"`
	Timezone    string          `json:"timezone"`
	ActionType  string          `json:"action_type"`
	Action      string          `json:"action"`
	Description string          `json:"description,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Enabled     *bool           `json:"enabled,omitempty"`
}

// cronPlan is what a pg_cron job becomes.
type cronPlan struct {
	Source  pgCronJob
	Request cronJobRequest
	// FunctionURL is where a function job's call went on Supabase,
	// without its query string.
	FunctionURL string
	// Problems are why the job can't be created as it is.
	Problems []string
	// Notes are things to know about a job that can.
	Notes []string
}

func (p *cronPlan) Ready() bool { return len(p.Problems) == 0 }

// planCronJobs plans every job and gives two jobs with the same name
// (pg_cron allows that across database users) names of their own.
func planCronJobs(jobs []pgCronJob, timezone string) []*cronPlan {
	seen := map[string]bool{}
	plans := make([]*cronPlan, 0, len(jobs))
	for _, j := range jobs {
		p := planCronJob(j, timezone)
		if seen[p.Request.Name] {
			renamed := fmt.Sprintf("%s-%d", p.Request.Name, j.ID)
			p.Notes = append(p.Notes, fmt.Sprintf("another job is also called %q; this one is created as %q", p.Request.Name, renamed))
			p.Request.Name = renamed
		}
		seen[p.Request.Name] = true
		plans = append(plans, p)
	}
	return plans
}

// planCronJob works out the Eurobase job for one pg_cron job.
func planCronJob(j pgCronJob, timezone string) *cronPlan {
	p := &cronPlan{Source: j}
	name := strings.TrimSpace(j.Name)
	if name == "" {
		name = "job-" + strconv.FormatInt(j.ID, 10)
	}
	p.Request = cronJobRequest{
		Name:        name,
		Timezone:    timezone,
		Description: fmt.Sprintf("Imported from Supabase (pg_cron job %d)", j.ID),
	}

	schedule, note, err := translateCronSchedule(j.Schedule)
	if err != nil {
		p.Problems = append(p.Problems, "schedule: "+err.Error())
	} else {
		p.Request.Schedule = schedule
		if note != "" {
			p.Notes = append(p.Notes, note)
		}
	}

	// One statement, or nothing is planned: a job runs one.
	var statements []string
	for _, s := range splitStatements(j.Command) {
		if strings.Trim(strings.TrimSpace(statementCode(s)), "; \t\r\n") != "" {
			statements = append(statements, strings.TrimSpace(s))
		}
	}
	switch {
	case len(statements) == 0:
		p.Request.ActionType = "sql"
		p.Problems = append(p.Problems, "the command is empty")
		return p
	case len(statements) > 1:
		p.Request.ActionType = "sql"
		p.Problems = append(p.Problems, fmt.Sprintf("the command has %d statements; a Eurobase cron job runs one — put them into one database function and schedule SELECT that_function()", len(statements)))
		return p
	}
	statement := strings.TrimSuffix(strings.TrimSpace(withoutComments(statements[0])), ";")
	if call, ok := parseHTTPCall(statement); ok {
		planFunctionJob(p, call)
		return p
	}
	planSQLJob(p, statement)
	return p
}

// withoutComments returns the statement with its comments removed and
// its strings and quoted names untouched.
func withoutComments(sql string) string {
	segs, _ := splitCodeAndLiterals(sql)
	var b strings.Builder
	for _, seg := range segs {
		if !seg.code && (strings.HasPrefix(seg.text, "--") || strings.HasPrefix(seg.text, "/*")) {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(seg.text)
	}
	return strings.TrimSpace(b.String())
}

// ── SQL jobs ────────────────────────────────────────────────────────

var (
	// Schemas only a Supabase database has, bare or quoted.
	supabaseSchemaRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_$".])"?(auth|cron|net|vault|storage|extensions|realtime|supabase_functions|pgsodium|graphql|graphql_public|pgmq|pgbouncer)"?\s*\.\s*["a-z_]`)
	// name.name( — a function in another schema. A column of a table
	// alias is never followed by "(".
	qualifiedCallRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_$".])"?([a-z_][a-z0-9_$]*)"?\s*\.\s*"?[a-z_][a-z0-9_$]*"?\s*\(`)
	// from/join/into/update/table name.name — a table in another schema.
	qualifiedTableRe = regexp.MustCompile(`(?i)\b(?:from|join|into|update|table|only)\s+"?([a-z_][a-z0-9_$]*)"?\s*\.\s*"?[a-z_]`)
	// The pgsql-http extension's functions, called without a schema.
	pgsqlHTTPRe  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_$".])http(_get|_post|_put|_patch|_delete|_head)?\s*\(`)
	firstWordRe  = regexp.MustCompile(`(?i)^[\s(]*([a-z]+)`)
	runnableKind = map[string]bool{"select": true, "insert": true, "update": true, "delete": true, "with": true, "call": true, "merge": true, "values": true}
)

func planSQLJob(p *cronPlan, statement string) {
	p.Request.ActionType = "sql"
	kind := ""
	if m := firstWordRe.FindStringSubmatch(statementCode(statement)); m != nil {
		kind = strings.ToLower(m[1])
	}
	if !runnableKind[kind] {
		p.Problems = append(p.Problems, fmt.Sprintf("a %s statement can't run as a cron job: a job runs SELECT, INSERT, UPDATE, DELETE or CALL, inside a transaction, with your project's function role (maintenance such as VACUUM and ANALYZE is done by the platform)", strings.ToUpper(kind)))
		return
	}

	res := Translate(statement)
	action := strings.TrimSuffix(strings.TrimSpace(res.sql), ";")
	p.Request.Action = action
	for _, w := range res.warnings {
		// The translator's notes are written for a schema file; only
		// those about access matter for one statement.
		if w.access {
			p.Notes = append(p.Notes, w.note)
		}
	}
	translated := statementCode(action)
	aliases := tableAliases(translated)
	refuse := func(reason string) {
		p.Problems = append(p.Problems, "the statement can't run as a cron job: "+reason)
	}
	if err := cron.CheckSQLAction(action); err != nil {
		refuse(err.Error())
		return
	}
	if m := supabaseSchemaRe.FindStringSubmatch(translated); m != nil && !aliases[strings.ToLower(m[1])] {
		refuse(fmt.Sprintf("it uses %s.…, which is part of Supabase, not of your tables — a Eurobase project has no %q schema", strings.ToLower(m[1]), strings.ToLower(m[1])))
		return
	}
	for _, re := range []*regexp.Regexp{qualifiedCallRe, qualifiedTableRe} {
		for _, m := range re.FindAllStringSubmatch(translated, -1) {
			schema := strings.ToLower(m[1])
			if schema == "pg_temp" || aliases[schema] {
				continue
			}
			refuse(fmt.Sprintf("it uses the schema %q; a Eurobase project's tables and functions are in one schema — bring that object over with the schema import first, then schedule the statement without the prefix", schema))
			return
		}
	}
	if pgsqlHTTPRe.MatchString(translated) {
		refuse("it makes an HTTP request from the database (the http extension), which Eurobase doesn't have — make an edge function that calls the address with fetch, and schedule the function")
	}
}

var aliasRe = regexp.MustCompile(`(?i)\b(?:from|join|update|into|using)\s+(?:only\s+)?(?:"?[a-z_][a-z0-9_$]*"?\s*\.\s*)?"?([a-z_][a-z0-9_$]*)"?(?:\s+(?:as\s+)?"?([a-z_][a-z0-9_$]*)"?)?`)

// tableAliases returns the names a statement uses for its tables: the
// tables themselves and their aliases. "net.amount" is a column when the
// statement says "FROM invoices net".
func tableAliases(code string) map[string]bool {
	out := map[string]bool{}
	keywords := map[string]bool{"where": true, "set": true, "on": true, "join": true, "left": true, "right": true, "inner": true, "outer": true, "cross": true, "full": true, "group": true, "order": true, "limit": true, "using": true, "values": true, "select": true, "returning": true, "natural": true, "as": true}
	for _, m := range aliasRe.FindAllStringSubmatch(code, -1) {
		for _, name := range m[1:] {
			name = strings.ToLower(name)
			if name != "" && !keywords[name] {
				out[name] = true
			}
		}
	}
	return out
}

// ── calls to edge functions (pg_net) ────────────────────────────────

// httpCall is a command that is one net.http_post(…) / net.http_get(…).
type httpCall struct {
	Method string
	// URL is the address when the command gives it as one fixed text.
	URL        string
	DynamicURL bool
	// Body is the JSON body when the command gives it as a fixed text.
	Body        json.RawMessage
	DynamicBody bool
	// HeaderNames are the request headers' names; values are never kept.
	HeaderNames    []string
	DynamicHeaders bool
}

var (
	// The whole statement: SELECT net.http_x( … ) [AS name]
	httpCallRe  = regexp.MustCompile(`(?is)^\s*select\s+"?net"?\s*\.\s*"?http_(post|get|put|patch|delete)"?\s*\(`)
	httpTailRe  = regexp.MustCompile(`(?is)^\s*((as\s+)?"?[a-z_][a-z0-9_]*"?)?\s*;?\s*$`)
	namedArgRe  = regexp.MustCompile(`(?is)^\s*"?([a-z_][a-z0-9_]*)"?\s*(:=|=>)\s*(.*)$`)
	textLiteral = regexp.MustCompile(`(?is)^'((?:[^']|'')*)'\s*(::\s*(text|varchar|character varying))?$`)
)

// pg_net's parameters, in order.
var pgNetParams = map[string][]string{
	"post":   {"url", "body", "params", "headers", "timeout_milliseconds"},
	"get":    {"url", "params", "headers", "timeout_milliseconds"},
	"delete": {"url", "params", "headers", "timeout_milliseconds"},
	"put":    {"url", "body", "params", "headers", "timeout_milliseconds"},
	"patch":  {"url", "body", "params", "headers", "timeout_milliseconds"},
}

// parseHTTPCall recognises a statement that is nothing but one pg_net
// request. A request inside other SQL (a CTE, a DO block, a second
// statement) is not one: turning that into a function job would drop the
// rest of the command.
func parseHTTPCall(statement string) (httpCall, bool) {
	m := httpCallRe.FindStringSubmatchIndex(statement)
	if m == nil {
		return httpCall{}, false
	}
	open := m[1] - 1
	end := findMatchingParen(statement, open)
	if end < 0 || !httpTailRe.MatchString(statement[end+1:]) {
		return httpCall{}, false
	}
	call := httpCall{Method: strings.ToLower(statement[m[2]:m[3]])}
	params := pgNetParams[call.Method]
	position := 0
	for _, arg := range splitArguments(statement[open+1 : end]) {
		name, expr := "", arg
		if nm := namedArgRe.FindStringSubmatch(arg); nm != nil {
			name, expr = strings.ToLower(nm[1]), strings.TrimSpace(nm[3])
		} else {
			if position < len(params) {
				name = params[position]
			}
			position++
		}
		switch name {
		case "url":
			if tm := textLiteral.FindStringSubmatch(strings.TrimSpace(expr)); tm != nil {
				call.URL = strings.ReplaceAll(tm[1], "''", "'")
			} else {
				call.DynamicURL = true
			}
		case "body":
			if text, ok := jsonLiteral(expr); ok {
				call.Body = json.RawMessage(text)
			} else {
				call.DynamicBody = true
			}
		case "headers":
			text, ok := jsonLiteral(expr)
			var headers map[string]json.RawMessage
			if ok && json.Unmarshal([]byte(text), &headers) == nil {
				for k := range headers {
					call.HeaderNames = append(call.HeaderNames, strings.TrimSpace(k))
				}
				sort.Strings(call.HeaderNames)
			} else {
				call.DynamicHeaders = true
			}
		}
	}
	return call, true
}

// splitArguments splits a call's argument text at its top-level commas:
// not inside brackets, and never inside a string or a quoted name.
func splitArguments(args string) []string {
	var out []string
	depth, start, pos := 0, 0, 0
	segs, _ := splitCodeAndLiterals(args)
	for _, seg := range segs {
		if seg.code {
			for i := 0; i < len(seg.text); i++ {
				switch seg.text[i] {
				case '(', '[':
					depth++
				case ')', ']':
					depth--
				case ',':
					if depth == 0 {
						out = append(out, strings.TrimSpace(args[start:pos+i]))
						start = pos + i + 1
					}
				}
			}
		}
		pos += len(seg.text)
	}
	if rest := strings.TrimSpace(args[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

var jsonLiteralRe = regexp.MustCompile(`(?is)^'((?:[^']|'')*)'\s*(::\s*jsonb?)?$`)

// jsonLiteral returns the JSON in an expression that is nothing but a
// quoted JSON text, with or without a ::jsonb cast.
func jsonLiteral(expr string) (string, bool) {
	m := jsonLiteralRe.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return "", false
	}
	text := strings.ReplaceAll(m[1], "''", "'")
	if !json.Valid([]byte(text)) {
		return "", false
	}
	return text, true
}

var (
	// functionPathRe is the whole path of a call to an edge function.
	functionPathRe = regexp.MustCompile(`^/functions/v1/([^/]+)/?$`)
	// supabaseHostRe: a hosted project's address, or a local stack's.
	supabaseHostRe = regexp.MustCompile(`(?i)(\.supabase\.(co|in|red)$|^localhost$|^127\.0\.0\.1$|^kong$|^host\.docker\.internal$|supabase[_-]kong)`)
)

func planFunctionJob(p *cronPlan, call httpCall) {
	p.Request.ActionType = "function"
	notAFunction := func(what string) {
		p.Problems = append(p.Problems, fmt.Sprintf("it calls %s from the database (pg_net), which Eurobase doesn't have — make an edge function that calls that address with fetch, and schedule the function", what))
	}
	if call.DynamicURL || call.URL == "" {
		p.Problems = append(p.Problems, "the address it calls is built in SQL, so it can't be told which function it is — create the job by hand (console → Cron) for the function it calls")
		return
	}
	u, err := url.Parse(call.URL)
	if err != nil || u.Host == "" {
		notAFunction("an address")
		return
	}
	shown := u.Scheme + "://" + u.Host + u.Path
	m := functionPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		if strings.HasPrefix(u.Path, "/functions/v1/") {
			p.Problems = append(p.Problems, fmt.Sprintf("it calls the function with a path below its name (%s); a cron job calls the function itself — have the function take that from its payload", shown))
			return
		}
		notAFunction(shown)
		return
	}
	if u.RawQuery != "" {
		p.Problems = append(p.Problems, fmt.Sprintf("it calls the function with a query string (%s?…); a cron job can't pass one — have the function take those values from its payload", shown))
		return
	}
	name := m[1]
	if err := cron.CheckFunctionName(name); err != nil {
		p.Problems = append(p.Problems, fmt.Sprintf("function name %q: %v", name, err))
		return
	}
	p.Request.Action = name
	p.FunctionURL = shown
	if !supabaseHostRe.MatchString(u.Hostname()) {
		p.Notes = append(p.Notes, fmt.Sprintf("the call went to %s — make sure that is this project's own function, not another service", u.Host))
	}
	if call.Method != "post" {
		p.Notes = append(p.Notes, fmt.Sprintf("Supabase called the function with %s; a cron job calls it with POST", strings.ToUpper(call.Method)))
	}
	if call.Body != nil {
		p.Request.Payload = call.Body
	} else if call.DynamicBody {
		p.Notes = append(p.Notes, "the request body was built in SQL (not a fixed JSON text), so it isn't carried over — the function is called with an empty body; set the job's payload, or let the function work the values out")
	}
	switch {
	case call.DynamicHeaders:
		p.Notes = append(p.Notes, "the request headers (built in SQL) aren't carried over: a cron job calls the function directly, without a key")
	case len(call.HeaderNames) > 0:
		p.Notes = append(p.Notes, fmt.Sprintf("the request headers aren't carried over (%s): a cron job calls the function directly, without a key — if the function needs one of them, set it on the job in the console", shortLine(strings.Join(call.HeaderNames, ", "), 80)))
	}
}

// ── schedules ───────────────────────────────────────────────────────

type cronField struct {
	name     string
	min, max int
	names    map[string]int
}

var pgCronFields = []cronField{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}},
	{name: "day of week", min: 0, max: 6, names: map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}},
}

var (
	secondsScheduleRe = regexp.MustCompile(`(?i)^\s*(\d+)\s+seconds?\s*$`)
	cronMacros        = map[string]string{
		"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
		"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
	}
	digitsRe = regexp.MustCompile(`^[0-9]+$`)
)

// translateCronSchedule rewrites a pg_cron schedule in the syntax
// Eurobase's scheduler reads: per field "*", "*/N", a number, or a comma
// list of numbers. Ranges, steps over ranges and names are written out.
//
// The day of the month and the day of the week need care. Eurobase runs
// a job when both match. cron does the same when either field begins
// with "*"; when neither does, it runs the job when EITHER matches. Such
// a schedule is only carried over when one of the two covers every day
// (then "either" is every day); otherwise it is refused.
func translateCronSchedule(schedule string) (string, string, error) {
	s := strings.TrimSpace(schedule)
	if m := secondsScheduleRe.FindStringSubmatch(s); m != nil {
		return "", "", fmt.Errorf("%q runs every %s seconds; the shortest interval of a Eurobase cron job is one minute (* * * * *)", s, m[1])
	}
	if strings.EqualFold(s, "@reboot") {
		return "", "", errors.New("\"@reboot\" runs when the database server starts; a Eurobase cron job runs at times")
	}
	if macro, ok := cronMacros[strings.ToLower(s)]; ok {
		s = macro
	}
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return "", "", fmt.Errorf("%q doesn't have five fields (minute hour day-of-month month day-of-week)", schedule)
	}
	out := make([]string, 5)
	all := make([]bool, 5)
	for i, field := range fields {
		f, every, err := translateCronField(field, pgCronFields[i])
		if err != nil {
			return "", "", fmt.Errorf("%s %q: %v", pgCronFields[i].name, field, err)
		}
		out[i], all[i] = f, every
	}
	domStar, dowStar := strings.HasPrefix(fields[2], "*"), strings.HasPrefix(fields[4], "*")
	if !domStar && !dowStar {
		if !all[2] && !all[4] {
			return "", "", fmt.Errorf("%q limits both the day of the month and the day of the week; pg_cron runs it when either matches, which one Eurobase schedule can't say (it would run only when both match) — make one job for the days of the month and one for the weekdays, and mind that on a day that is both, both run", strings.TrimSpace(schedule))
		}
		// "Either matches" with one of them matching every day.
		out[2], out[4] = "*", "*"
	}
	result := strings.Join(out, " ")
	if err := cron.CheckSchedule(result); err != nil {
		return "", "", err
	}
	note := ""
	if result != strings.Join(fields, " ") {
		note = fmt.Sprintf("schedule written as %q (on Supabase: %q)", result, strings.TrimSpace(schedule))
	}
	return result, note, nil
}

// translateCronField returns the field in the scheduler's syntax and
// whether it matches every value.
func translateCronField(field string, f cronField) (string, bool, error) {
	if field == "*" {
		return "*", true, nil
	}
	if strings.Contains(field, "$") {
		return "", false, errors.New("\"$\" (the last day of the month) has no equivalent; schedule days 28-31 and let the job check the date")
	}
	weekday := f.name == "day of week"
	// A weekday may be written 0-7, with 0 and 7 both Sunday.
	top := f.max
	if weekday {
		top = 7
	}
	value := func(s string) (int, error) {
		if n, ok := f.names[strings.ToLower(s)]; ok {
			return n, nil
		}
		if !digitsRe.MatchString(s) {
			return 0, fmt.Errorf("%q is not a number", s)
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < f.min || n > top {
			return 0, fmt.Errorf("%s is outside %d-%d", s, f.min, f.max)
		}
		return n, nil
	}
	parts := strings.Split(field, ",")
	set := map[int]bool{}
	add := func(v int) {
		if weekday {
			v %= 7
		}
		set[v] = true
	}
	for _, part := range parts {
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			if !digitsRe.MatchString(stepText) {
				return "", false, fmt.Errorf("step %q is not a number from 1", stepText)
			}
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return "", false, fmt.Errorf("step %q is not a number from 1", stepText)
			}
			step = n
		}
		if base == "*" {
			// "*/N" means the same in both for fields that start at 0:
			// every value divisible by N. Days and months start at 1,
			// where cron counts 1, 1+N, …; those are written out.
			if hasStep && len(parts) == 1 && f.min == 0 {
				return "*/" + strconv.Itoa(step), step == 1, nil
			}
			for v := f.min; v <= f.max; v += step {
				add(v)
			}
			continue
		}
		lo, hi := 0, 0
		if a, b, isRange := strings.Cut(base, "-"); isRange {
			var err error
			if lo, err = value(a); err != nil {
				return "", false, err
			}
			if hi, err = value(b); err != nil {
				return "", false, err
			}
			if hi < lo {
				return "", false, fmt.Errorf("range %q runs backwards", base)
			}
		} else {
			var err error
			if lo, err = value(base); err != nil {
				return "", false, err
			}
			hi = lo
			if hasStep {
				hi = f.max // "5/15" is from 5 on, every 15
			}
		}
		for v := lo; v <= hi; v += step {
			add(v)
		}
	}
	if len(set) == f.max-f.min+1 {
		return "*", true, nil
	}
	values := make([]int, 0, len(set))
	for v := range set {
		values = append(values, v)
	}
	sort.Ints(values)
	texts := make([]string, len(values))
	for i, v := range values {
		texts[i] = strconv.Itoa(v)
	}
	return strings.Join(texts, ","), false, nil
}
