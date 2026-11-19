package cli

// `eurobase import supabase cron` — carry a Supabase project's scheduled
// jobs (the pg_cron extension, cron.job) over to Eurobase cron jobs.
//
// A pg_cron job is a schedule and a SQL command. Two kinds come over:
//
//   - a command that calls an edge function over HTTP
//     (net.http_post('https://<ref>.supabase.co/functions/v1/<name>', …))
//     becomes a job that runs the function <name>, with the JSON body as
//     its payload
//   - any other single statement becomes a SQL job, translated the way
//     the schema import translates SQL (no "public." prefix, …)
//
// What can't be carried over is listed with the reason: several
// statements in one command, a call to a URL that isn't one of the
// project's functions, SQL that reaches outside the project's tables, a
// schedule in seconds.
//
// The command reads the source and prints the plan. With --apply it
// creates the jobs in the active project — switched off, because the
// same jobs are still running on Supabase until the switch-over.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		apply  bool
		enable bool
	)
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Carry a Supabase project's scheduled jobs (pg_cron) over to Eurobase cron jobs",
		Long: `Read the scheduled jobs of a Supabase project (pg_cron's cron.job) and
show how each becomes a Eurobase cron job. With --apply, create them in
the active project.

  SQL command                         → a SQL job with the same statement
  net.http_post(…/functions/v1/name)  → a job that runs the function "name"

Schedules are rewritten where the two differ (a range such as 1-5 becomes
1,2,3,4,5; MON becomes 1). The time zone is the source's cron.timezone
(UTC unless you changed it).

Jobs are created switched off: the same jobs keep running on Supabase
until you switch over, and most must not run twice. Switch them on in the
console (Cron) when you do, or pass --enable to create them as they are
on Supabase.

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

			jobs, timezone, err := readPgCronJobs(ctx, tx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(jobs) == 0 {
				fmt.Fprintln(out, "No scheduled jobs: the source has no pg_cron jobs (cron.job is empty or the extension isn't installed).")
				return nil
			}

			plans := make([]*cronPlan, 0, len(jobs))
			ready := 0
			for _, j := range jobs {
				p := planCronJob(j, timezone)
				plans = append(plans, p)
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
						fmt.Fprintf(out, "  %-36s – already there, left as it is\n", req.Name)
						continue
					}
					failed++
					fmt.Fprintf(out, "  %-36s ✗ %v\n", req.Name, err)
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
	return cmd
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
	what := "SQL: " + shortLine(p.Request.Action, 90)
	if p.Request.ActionType == "function" {
		what = "runs the function " + p.Request.Action
		if len(p.Request.Payload) > 0 {
			what += " with " + shortLine(string(p.Request.Payload), 60)
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

var (
	bearerRe = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	jwtRe    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9._-]{8,}`)
)

// withoutKeys hides what looks like a key in a command before it is
// printed: pg_cron commands that call a function carry one in a header.
func withoutKeys(s string) string {
	s = bearerRe.ReplaceAllString(s, "${1}…")
	return jwtRe.ReplaceAllString(s, "…")
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
	if len(s) > max {
		return s[:max-1] + "…"
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

// ── planning one job ────────────────────────────────────────────────

// cronJobRequest is the body of POST …/cron.
type cronJobRequest struct {
	Name        string          `json:"name"`
	Schedule    string          `json:"schedule"`
	Timezone    string          `json:"timezone"`
	ActionType  string          `json:"action_type"`
	Action      string          `json:"action"`
	Description string          `json:"description,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Headers     json.RawMessage `json:"headers,omitempty"`
	Enabled     *bool           `json:"enabled,omitempty"`
}

// cronPlan is what a pg_cron job becomes.
type cronPlan struct {
	Source  pgCronJob
	Request cronJobRequest
	// Problems are why the job can't be created as it is.
	Problems []string
	// Notes are things to know about a job that can.
	Notes []string
}

func (p *cronPlan) Ready() bool { return len(p.Problems) == 0 }

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

	command := strings.TrimSpace(j.Command)
	if call, ok := parseHTTPCall(command); ok {
		planFunctionJob(p, call)
		return p
	}
	planSQLJob(p, command)
	return p
}

func planSQLJob(p *cronPlan, command string) {
	p.Request.ActionType = "sql"
	statements := splitStatements(command)
	var kept []string
	for _, s := range statements {
		if strings.TrimSpace(statementCode(s)) != "" {
			kept = append(kept, strings.TrimSpace(s))
		}
	}
	switch {
	case len(kept) == 0:
		p.Problems = append(p.Problems, "the command is empty")
		return
	case len(kept) > 1:
		p.Problems = append(p.Problems, fmt.Sprintf("the command has %d statements; a Eurobase cron job runs one — put them into one database function and schedule SELECT that_function()", len(kept)))
		return
	}
	res := Translate(strings.TrimSuffix(strings.TrimSpace(kept[0]), ";"))
	action := strings.TrimSuffix(strings.TrimSpace(res.sql), ";")
	p.Request.Action = action
	for _, w := range res.warnings {
		p.Notes = append(p.Notes, w.note)
	}
	if err := cron.CheckSQLAction(action); err != nil {
		p.Problems = append(p.Problems, "the statement can't run as a cron job: "+err.Error())
	}
	// Schemas only a Supabase database has. The server's check doesn't
	// name them (they simply don't exist here); the job would fail every
	// time it runs.
	if m := supabaseSchemaRe.FindStringSubmatch(statementCode(action)); m != nil {
		p.Problems = append(p.Problems, fmt.Sprintf("the statement can't run as a cron job: it uses %s.…, which is part of Supabase, not of your tables — a Eurobase project has no %q schema", strings.ToLower(m[1]), strings.ToLower(m[1])))
	}
}

var supabaseSchemaRe = regexp.MustCompile(`(?i)\b(auth|cron|net|vault|storage|extensions|realtime|supabase_functions|pgsodium|graphql|graphql_public|pgmq|pgbouncer)\s*\.\s*[a-z_"]`)

func planFunctionJob(p *cronPlan, call httpCall) {
	p.Request.ActionType = "function"
	if call.Function == "" {
		p.Problems = append(p.Problems, fmt.Sprintf("it calls %s from the database (pg_net), which Eurobase doesn't have — make an edge function that calls that address with fetch, and schedule the function", call.shownURL()))
		return
	}
	if err := cron.CheckFunctionName(call.Function); err != nil {
		p.Problems = append(p.Problems, fmt.Sprintf("function name %q: %v", call.Function, err))
		return
	}
	p.Request.Action = call.Function
	if call.Method != "post" {
		p.Notes = append(p.Notes, fmt.Sprintf("Supabase called the function with %s; a cron job calls it with POST", strings.ToUpper(call.Method)))
	}
	if call.Body != nil {
		p.Request.Payload = call.Body
	} else if call.DynamicBody {
		p.Notes = append(p.Notes, "the request body was built in SQL (not a fixed JSON text), so it isn't carried over — the function is called with an empty body; set the job's payload, or let the function work the values out")
	}
	if len(call.Headers) > 0 {
		b, _ := json.Marshal(call.Headers)
		p.Request.Headers = b
	}
	if call.DroppedAuth {
		p.Notes = append(p.Notes, "the Authorization / apikey header isn't carried over: a cron job calls the function directly, without a key")
	}
	if call.DynamicHeaders {
		p.Notes = append(p.Notes, "the request headers were built in SQL and aren't carried over")
	}
}

// ── calls to edge functions (pg_net) ────────────────────────────────

// httpCall is a net.http_post(…) / net.http_get(…) command.
type httpCall struct {
	Method string
	URL    string
	// Function is the edge function the URL names; "" for any other URL.
	Function string
	// Body is the JSON body when the command gives it as a fixed text.
	Body        json.RawMessage
	DynamicBody bool
	// Headers are the fixed headers, without the ones that carried a key.
	Headers        map[string]string
	DynamicHeaders bool
	DroppedAuth    bool
}

func (c httpCall) shownURL() string {
	if c.URL == "" {
		return "a URL"
	}
	return c.URL
}

var (
	httpCallRe    = regexp.MustCompile(`(?is)\bnet\s*\.\s*http_(post|get|put|patch|delete)\s*\(`)
	urlLiteralRe  = regexp.MustCompile(`(?is)https?://[^'"\s)]+`)
	functionURLRe = regexp.MustCompile(`(?i)/functions/v1/([A-Za-z0-9_-]+)`)
)

// parseHTTPCall recognises a command that makes an HTTP request with
// pg_net and reads what is fixed in it.
func parseHTTPCall(command string) (httpCall, bool) {
	m := httpCallRe.FindStringSubmatchIndex(command)
	if m == nil {
		return httpCall{}, false
	}
	call := httpCall{Method: strings.ToLower(command[m[2]:m[3]])}
	open := m[1] - 1
	end := findMatchingParen(command, open)
	if end < 0 {
		end = len(command)
	}
	args := command[open+1 : end]

	if u := urlLiteralRe.FindString(args); u != "" {
		call.URL = u
		if f := functionURLRe.FindStringSubmatch(u); f != nil {
			call.Function = f[1]
		}
	}

	if expr, ok := namedArgument(args, "body"); ok {
		if text, ok := jsonLiteral(expr); ok {
			call.Body = json.RawMessage(text)
		} else {
			call.DynamicBody = true
		}
	}
	if expr, ok := namedArgument(args, "headers"); ok {
		text, ok := jsonLiteral(expr)
		var headers map[string]string
		if ok && json.Unmarshal([]byte(text), &headers) == nil {
			call.Headers = map[string]string{}
			for k, v := range headers {
				switch strings.ToLower(k) {
				case "authorization", "apikey":
					call.DroppedAuth = true
				case "content-type":
					// The job sends JSON anyway.
				default:
					call.Headers[k] = v
				}
			}
		} else {
			call.DynamicHeaders = true
			// A key in a built header is the usual reason it is built.
			call.DroppedAuth = true
		}
	}
	return call, true
}

// namedArgument returns the expression given for `name := …` (or
// `name => …`) in a function call's argument text.
func namedArgument(args, name string) (string, bool) {
	re := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(name) + `\s*(:=|=>)\s*`)
	loc := re.FindStringIndex(args)
	if loc == nil {
		return "", false
	}
	rest := args[loc[1]:]
	// Up to the comma that ends this argument: not inside quotes or
	// brackets.
	depth, inString := 0, false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case inString:
			if c == '\'' {
				if i+1 < len(rest) && rest[i+1] == '\'' {
					i++
				} else {
					inString = false
				}
			}
		case c == '\'':
			inString = true
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ',' && depth == 0:
			return strings.TrimSpace(rest[:i]), true
		}
	}
	return strings.TrimSpace(rest), true
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
)

// translateCronSchedule rewrites a pg_cron schedule in the syntax
// Eurobase's scheduler reads: per field "*", "*/N", a number, or a comma
// list of numbers. Ranges, steps over ranges and names are written out.
func translateCronSchedule(schedule string) (string, string, error) {
	s := strings.TrimSpace(schedule)
	if m := secondsScheduleRe.FindStringSubmatch(s); m != nil {
		return "", "", fmt.Errorf("%q runs every %s seconds; the shortest interval of a Eurobase cron job is one minute (* * * * *)", s, m[1])
	}
	if macro, ok := cronMacros[strings.ToLower(s)]; ok {
		s = macro
	}
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return "", "", fmt.Errorf("%q doesn't have five fields (minute hour day-of-month month day-of-week)", schedule)
	}
	out := make([]string, 5)
	for i, field := range fields {
		f, err := translateCronField(field, pgCronFields[i])
		if err != nil {
			return "", "", fmt.Errorf("%s %q: %v", pgCronFields[i].name, field, err)
		}
		out[i] = f
	}
	result := strings.Join(out, " ")
	if err := cron.CheckSchedule(result); err != nil {
		return "", "", err
	}
	note := ""
	if out[2] != "*" && out[4] != "*" {
		// cron runs such a job when EITHER matches.
		return "", "", fmt.Errorf("%q limits both the day of the month and the day of the week; pg_cron runs it when either matches, which one Eurobase schedule can't say — make two jobs, one for each", schedule)
	}
	if result != strings.Join(fields, " ") {
		note = fmt.Sprintf("schedule written as %q (on Supabase: %q)", result, strings.TrimSpace(schedule))
	}
	return result, note, nil
}

func translateCronField(field string, f cronField) (string, error) {
	if field == "*" {
		return "*", nil
	}
	if strings.Contains(field, "$") {
		return "", errors.New("\"$\" (the last day of the month) has no equivalent; schedule days 28-31 and let the job check the date")
	}
	value := func(s string) (int, error) {
		if n, ok := f.names[strings.ToLower(s)]; ok {
			return n, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", s)
		}
		if f.name == "day of week" && n == 7 {
			n = 0
		}
		if n < f.min || n > f.max {
			return 0, fmt.Errorf("%d is outside %d-%d", n, f.min, f.max)
		}
		return n, nil
	}
	set := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return "", fmt.Errorf("step %q is not a number from 1", stepText)
			}
			step = n
		}
		if base == "*" {
			if hasStep && len(strings.Split(field, ",")) == 1 {
				// The same meaning in both: every value divisible by N.
				// (Only for fields that start at 0; day and month start
				// at 1, where cron counts from 1.)
				if f.min == 0 {
					return "*/" + strconv.Itoa(step), nil
				}
			}
			for v := f.min; v <= f.max; v += step {
				set[v] = true
			}
			continue
		}
		lo, hi := 0, 0
		if a, b, isRange := strings.Cut(base, "-"); isRange {
			var err error
			if lo, err = value(a); err != nil {
				return "", err
			}
			if hi, err = value(b); err != nil {
				return "", err
			}
			if f.name == "day of week" && b == "7" {
				// "5-7" is Friday to Sunday.
				for v := lo; v <= 6; v += step {
					set[v] = true
				}
				set[0] = true
				continue
			}
			if hi < lo {
				return "", fmt.Errorf("range %q runs backwards", base)
			}
		} else {
			var err error
			if lo, err = value(base); err != nil {
				return "", err
			}
			hi = lo
			if hasStep {
				hi = f.max // "5/15" is from 5 on, every 15
			}
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	if len(set) == f.max-f.min+1 {
		return "*", nil
	}
	values := make([]int, 0, len(set))
	for v := range set {
		values = append(values, v)
	}
	sort.Ints(values)
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ","), nil
}
