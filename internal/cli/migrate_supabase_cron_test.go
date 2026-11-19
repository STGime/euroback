package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/cron"
	"github.com/jackc/pgx/v5"
)

func TestTranslateCronSchedule(t *testing.T) {
	for in, want := range map[string]string{
		"*/5 * * * *":     "*/5 * * * *",
		"0 3 * * *":       "0 3 * * *",
		"0 9 * * 1-5":     "0 9 * * 1,2,3,4,5",
		"0 9 * * MON-FRI": "0 9 * * 1,2,3,4,5",
		"0 0 * * 7":       "0 0 * * 0",
		"0 0 * * 5-7":     "0 0 * * 0,5,6",
		"0 0 1 JAN,jul *": "0 0 1 1,7 *",
		"0-30/10 * * * *": "0,10,20,30 * * * *",
		"5/20 * * * *":    "5,25,45 * * * *",
		"0 8-18/2 * * *":  "0 8,10,12,14,16,18 * * *",
		// A step on days and months counts from 1 in cron (1,3,5,…); the
		// scheduler's */N means "divisible by N", so these are written out.
		"0 0 */10 * *":       "0 0 1,11,21,31 * *",
		"0 0 1 */3 *":        "0 0 1 1,4,7,10 *",
		"0 0 * * */2":        "0 0 * * */2",
		"0-59 * * * *":       "* * * * *",
		"@daily":             "0 0 * * *",
		"@hourly":            "0 * * * *",
		"  30  4  *  *  *  ": "30 4 * * *",
	} {
		got, _, err := translateCronSchedule(in)
		if err != nil || got != want {
			t.Errorf("translateCronSchedule(%q) = %q, %v; want %q", in, got, err, want)
			continue
		}
		// Whatever comes out, the server accepts.
		if err := cron.CheckSchedule(got); err != nil {
			t.Errorf("%q → %q is refused by the scheduler's check: %v", in, got, err)
		}
	}
	for in, wantErr := range map[string]string{
		"30 seconds":   "one minute",
		"0 0 $ * *":    "last day of the month",
		"0 0 13 * 5":   "either matches",
		"0 0 * *":      "five fields",
		"61 * * * *":   "outside 0-59",
		"0 0 * * 9":    "outside 0-6",
		"0 0 10-5 * *": "backwards",
		"*/0 * * * *":  "step",
		"a * * * *":    "not a number",
	} {
		if got, _, err := translateCronSchedule(in); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("translateCronSchedule(%q) = %q, %v; want an error mentioning %q", in, got, err, wantErr)
		}
	}
	// A rewritten schedule is pointed out; an unchanged one isn't.
	if _, note, _ := translateCronSchedule("0 9 * * 1-5"); !strings.Contains(note, `"0 9 * * 1,2,3,4,5"`) {
		t.Errorf("note for a rewritten schedule = %q", note)
	}
	if _, note, _ := translateCronSchedule("0 3 * * *"); note != "" {
		t.Errorf("note for an unchanged schedule = %q", note)
	}
}

func TestPlanCronJob_SQL(t *testing.T) {
	p := planCronJob(pgCronJob{ID: 4, Name: "purge-old", Schedule: "0 3 * * *", Command: "DELETE FROM public.sessions WHERE expires_at < now();", Active: true}, "UTC")
	if !p.Ready() || p.Request.ActionType != "sql" || p.Request.Action != "DELETE FROM sessions WHERE expires_at < now()" || p.Request.Name != "purge-old" || p.Request.Timezone != "UTC" {
		t.Errorf("plan = %+v problems=%v", p.Request, p.Problems)
	}
	// No name on the source (older pg_cron): the id names the job.
	if got := planCronJob(pgCronJob{ID: 9, Schedule: "* * * * *", Command: "select public.refresh_stats()"}, "Europe/Berlin"); got.Request.Name != "job-9" ||
		got.Request.Action != "select refresh_stats()" || got.Request.Timezone != "Europe/Berlin" || !got.Ready() {
		t.Errorf("unnamed job: %+v %v", got.Request, got.Problems)
	}
	// A text that looks like a qualified name is data, not a name.
	if got := planCronJob(pgCronJob{ID: 1, Name: "n", Schedule: "* * * * *", Command: "INSERT INTO log (msg) VALUES ('ran public.job; ok')"}, "UTC"); !got.Ready() ||
		got.Request.Action != "INSERT INTO log (msg) VALUES ('ran public.job; ok')" {
		t.Errorf("literal changed or split: %q %v", got.Request.Action, got.Problems)
	}
	for name, tc := range map[string]struct{ command, want string }{
		"two statements":  {"DELETE FROM a; DELETE FROM b;", "2 statements"},
		"another schema":  {"DELETE FROM auth.sessions WHERE not_after < now()", "can't run as a cron job"},
		"system catalog":  {"SELECT count(*) FROM pg_stat_activity", "can't run as a cron job"},
		"a DO block":      {"DO $$ BEGIN PERFORM 1; END $$", "can't run as a cron job"},
		"unschedule call": {"SELECT cron.unschedule('x')", "can't run as a cron job"},
		"empty":           {"  ;  ", "empty"},
	} {
		got := planCronJob(pgCronJob{ID: 1, Name: "j", Schedule: "* * * * *", Command: tc.command}, "UTC")
		if got.Ready() || !strings.Contains(strings.Join(got.Problems, "\n"), tc.want) {
			t.Errorf("%s: problems = %v, want one mentioning %q", name, got.Problems, tc.want)
		}
	}
}

func TestPlanCronJob_EdgeFunctionCalls(t *testing.T) {
	command := `
    select
      net.http_post(
          url:='https://abcdefgh.supabase.co/functions/v1/send-digest',
          headers:='{"Content-Type": "application/json", "Authorization": "Bearer eyJhbGciOi.fake.key", "X-Source": "cron"}'::jsonb,
          body:='{"kind": "daily", "note": "it''s time"}'::jsonb
      ) as request_id;`
	p := planCronJob(pgCronJob{ID: 2, Name: "digest", Schedule: "0 7 * * 1-5", Command: command, Active: true}, "UTC")
	if !p.Ready() {
		t.Fatalf("problems: %v", p.Problems)
	}
	if p.Request.ActionType != "function" || p.Request.Action != "send-digest" || p.Request.Schedule != "0 7 * * 1,2,3,4,5" {
		t.Errorf("request = %+v", p.Request)
	}
	if string(p.Request.Payload) != `{"kind": "daily", "note": "it's time"}` {
		t.Errorf("payload = %s", p.Request.Payload)
	}
	// The key is not carried over, and never appears in the plan.
	var headers map[string]string
	_ = json.Unmarshal(p.Request.Headers, &headers)
	if len(headers) != 1 || headers["X-Source"] != "cron" {
		t.Errorf("headers = %s", p.Request.Headers)
	}
	plan, _ := json.Marshal(p)
	if strings.Contains(string(plan), "eyJhbGciOi") && !strings.Contains(string(plan), `"command"`) {
		t.Error("the key reached the request")
	}
	reqJSON, _ := json.Marshal(p.Request)
	if strings.Contains(string(reqJSON), "eyJhbGciOi") {
		t.Errorf("the Supabase key is in the job that would be created: %s", reqJSON)
	}
	if !strings.Contains(strings.Join(p.Notes, "\n"), "Authorization / apikey header isn't carried over") {
		t.Errorf("notes = %v", p.Notes)
	}

	// A body or header built in SQL can't be read as a value.
	dynamic := planCronJob(pgCronJob{ID: 3, Name: "dyn", Schedule: "*/10 * * * *", Command: `select net.http_post(
		url := 'https://abcdefgh.supabase.co/functions/v1/sync_orders',
		headers := jsonb_build_object('Authorization', 'Bearer ' || (select decrypted_secret from vault.decrypted_secrets where name = 'service_key')),
		body := concat('{"time": "', now(), '"}')::jsonb)`}, "UTC")
	if !dynamic.Ready() || dynamic.Request.Action != "sync_orders" || dynamic.Request.Payload != nil || dynamic.Request.Headers != nil {
		t.Errorf("dynamic: %+v %v", dynamic.Request, dynamic.Problems)
	}
	notes := strings.Join(dynamic.Notes, "\n")
	if !strings.Contains(notes, "body was built in SQL") || !strings.Contains(notes, "headers were built in SQL") {
		t.Errorf("notes = %v", dynamic.Notes)
	}

	// A URL that isn't one of the project's functions has no place to go.
	other := planCronJob(pgCronJob{ID: 5, Name: "ping", Schedule: "* * * * *", Command: `select net.http_get('https://hooks.example.org/ping?key=abc')`}, "UTC")
	if other.Ready() || !strings.Contains(strings.Join(other.Problems, "\n"), "make an edge function") {
		t.Errorf("external URL: %v", other.Problems)
	}
	// A GET to a function still becomes a job, with a note.
	get := planCronJob(pgCronJob{ID: 6, Name: "g", Schedule: "* * * * *", Command: `select net.http_get(url => 'https://x.supabase.co/functions/v1/warm-up')`}, "UTC")
	if !get.Ready() || get.Request.Action != "warm-up" || !strings.Contains(strings.Join(get.Notes, "\n"), "with GET") {
		t.Errorf("GET to a function: %+v %v %v", get.Request, get.Problems, get.Notes)
	}
}

func TestJSONLiteralAndNamedArgument(t *testing.T) {
	args := `url := 'https://x/functions/v1/a', body := '{"a": [1, 2], "b": "x, y"}'::jsonb, timeout_milliseconds := 5000`
	if got, ok := namedArgument(args, "body"); !ok || got != `'{"a": [1, 2], "b": "x, y"}'::jsonb` {
		t.Errorf("body argument = %q %v", got, ok)
	}
	if got, ok := namedArgument(args, "timeout_milliseconds"); !ok || got != "5000" {
		t.Errorf("last argument = %q %v", got, ok)
	}
	if _, ok := namedArgument(args, "headers"); ok {
		t.Error("found an argument that isn't there")
	}
	for expr, want := range map[string]string{
		`'{"a":1}'::jsonb`:  `{"a":1}`,
		`'{"a":1}' :: json`: `{"a":1}`,
		`'[]'`:              `[]`,
		`'{"q":"it''s"}'`:   `{"q":"it's"}`,
	} {
		if got, ok := jsonLiteral(expr); !ok || got != want {
			t.Errorf("jsonLiteral(%q) = %q %v, want %q", expr, got, ok, want)
		}
	}
	for _, expr := range []string{`'{"a":1}'::jsonb || '{}'`, `jsonb_build_object('a', 1)`, `'not json'`, `concat('{', '}')::jsonb`} {
		if got, ok := jsonLiteral(expr); ok {
			t.Errorf("jsonLiteral(%q) = %q, want not a literal", expr, got)
		}
	}
}

// With a database: the jobs are read from a real cron.job table, however
// old its shape, and a source without pg_cron is no jobs, not an error.
func TestReadPgCronJobs_Fixture(t *testing.T) {
	dsn := os.Getenv("ASSESS_SCRATCH_DB_URL")
	if dsn == "" {
		t.Skip("set ASSESS_SCRATCH_DB_URL to a throwaway database to run this")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DROP SCHEMA IF EXISTS cron CASCADE`); err != nil {
		t.Fatal(err)
	}
	jobs, _, err := readPgCronJobs(ctx, tx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("no pg_cron: jobs=%v err=%v, want none and no error", jobs, err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE SCHEMA cron;
		CREATE TABLE cron.job (jobid bigserial PRIMARY KEY, schedule text NOT NULL, command text NOT NULL,
			nodename text DEFAULT 'localhost', nodeport int DEFAULT 5432, database text DEFAULT 'postgres',
			username text DEFAULT 'postgres', active boolean DEFAULT true, jobname text);
		INSERT INTO cron.job (schedule, command, jobname, active) VALUES
			('0 3 * * *', 'DELETE FROM public.sessions WHERE expires_at < now()', 'purge', true),
			('*/15 * * * 1-5', $c$select net.http_post(url:='https://ref.supabase.co/functions/v1/sync', body:='{"full": false}'::jsonb)$c$, 'sync', false),
			('30 seconds', 'select 1', NULL, true);
		SELECT set_config('cron.timezone', 'Europe/Stockholm', true);
	`); err != nil {
		t.Fatal(err)
	}
	jobs, tz, err := readPgCronJobs(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 || tz != "Europe/Stockholm" {
		t.Fatalf("jobs=%d tz=%q, want 3 and Europe/Stockholm", len(jobs), tz)
	}
	var got []string
	for _, j := range jobs {
		p := planCronJob(j, tz)
		state := "ready"
		if !p.Ready() {
			state = "needs changes"
		}
		got = append(got, strings.Join([]string{p.Request.Name, state, p.Request.ActionType, p.Request.Schedule, p.Request.Action}, "|"))
	}
	want := []string{
		"purge|ready|sql|0 3 * * *|DELETE FROM sessions WHERE expires_at < now()",
		"sync|ready|function|*/15 * * * 1,2,3,4,5|sync",
		"job-3|needs changes|sql||select 1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("plans:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if jobs[1].Active || !jobs[0].Active {
		t.Errorf("active flags: %v %v", jobs[0].Active, jobs[1].Active)
	}
	// An older pg_cron without the jobname column.
	if _, err := tx.Exec(ctx, `ALTER TABLE cron.job DROP COLUMN jobname`); err != nil {
		t.Fatal(err)
	}
	if jobs, _, err = readPgCronJobs(ctx, tx); err != nil || len(jobs) != 3 || jobs[0].Name != "" {
		t.Errorf("without jobname: %v %v", jobs, err)
	}
}
