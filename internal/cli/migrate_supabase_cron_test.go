package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

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
		"0 0 * * 0,7":     "0 0 * * 0",
		"0 0 1 JAN,jul *": "0 0 1 1,7 *",
		"0-30/10 * * * *": "0,10,20,30 * * * *",
		"5/20 * * * *":    "5,25,45 * * * *",
		"0 8-18/2 * * *":  "0 8,10,12,14,16,18 * * *",
		"*/15,7 * * * *":  "0,7,15,30,45 * * * *",
		// A step on days and months counts from 1 in cron (1,3,5,…); the
		// scheduler's */N means "divisible by N", so these are written out.
		"0 0 */10 * *":       "0 0 1,11,21,31 * *",
		"0 0 1 */3 *":        "0 0 1 1,4,7,10 *",
		"0 0 * * */2":        "0 0 * * */2",
		"0-59 * * * *":       "* * * * *",
		"@daily":             "0 0 * * *",
		"@hourly":            "0 * * * *",
		"  30  4  *  *  *  ": "30 4 * * *",
		// Weekday ranges that end in 7 (Sunday).
		"0 0 * * 7-7":   "0 0 * * 0",
		"0 0 * * 2-7/2": "0 0 * * 2,4,6",
		"0 0 * * 6-7/2": "0 0 * * 6",
		"0 0 * * 0-7":   "0 0 * * *",
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
		"0 0 1,15 * 1": "either matches",
		"0 0 * *":      "five fields",
		"61 * * * *":   "outside 0-59",
		"0 0 * * 9":    "outside 0-6",
		"0 0 10-5 * *": "backwards",
		"*/0 * * * *":  "step",
		"a * * * *":    "not a number",
		"+5 * * * *":   "not a number",
		"*/+2 * * * *": "step",
		"@reboot":      "database server starts",
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

// The translated schedule runs at the same times as the pg_cron one.
// Both rules are evaluated for every minute of two years: cron's (the
// day of the month and the weekday are ORed unless one of the two fields
// begins with "*") and the scheduler's (every field must match).
func TestTranslateCronSchedule_SameRunTimes(t *testing.T) {
	for _, schedule := range []string{
		"*/5 * * * *", "7 */6 * * *", "0 9 * * 1-5", "0 0 * * 5-7", "0 0 * * 7-7", "0 0 * * 2-7/2",
		"0 0 */10 * *", "0 0 1 */3 *", "0 0 * * */2", "*/15,7 * * * *", "30 4 1,15 * *",
		// Day of month and weekday together.
		"0 0 1-31 * 1",  // neither begins with *: either → every day
		"0 0 13 * 0-6",  // every weekday: every day
		"0 0 13 * 0-7",  // the same, written with 7
		"0 0 */2 * 1",   // begins with *: both must match
		"0 0 15 * */2",  // begins with *: both must match
		"0 0 */1 * 1",   // every day, on Mondays
		"0 0 1 * */1",   // the 1st, any weekday
		"0 12 * 2 0",    // Sundays in February
		"59 23 31 12 *", // once a year
	} {
		translated, _, err := translateCronSchedule(schedule)
		if err != nil {
			t.Errorf("%q: %v", schedule, err)
			continue
		}
		at := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		end := at.AddDate(2, 0, 0)
		step := time.Minute
		if strings.HasPrefix(schedule, "0 ") || strings.HasPrefix(schedule, "59 ") || strings.HasPrefix(schedule, "30 ") || strings.HasPrefix(schedule, "7 ") {
			step = time.Minute // every minute still: the minute field is part of the check
		}
		runs, mismatch := 0, 0
		for ; at.Before(end) && mismatch < 3; at = at.Add(step) {
			source, target := pgCronMatches(schedule, at), schedulerMatches(translated, at)
			if source {
				runs++
			}
			if source != target {
				mismatch++
				t.Errorf("%q → %q: at %s pg_cron runs=%v, Eurobase runs=%v", schedule, translated, at.Format("Mon 2006-01-02 15:04"), source, target)
			}
		}
		if runs == 0 {
			t.Errorf("%q never ran in two years: the reference rule is wrong", schedule)
		}
	}
}

// schedulerMatches is Eurobase's rule (internal/cron/executor.go): each
// field is "*", "*/N" (divisible by N), a number, or a list; all five
// must match.
func schedulerMatches(schedule string, at time.Time) bool {
	fields := strings.Fields(schedule)
	values := []int{at.Minute(), at.Hour(), at.Day(), int(at.Month()), int(at.Weekday())}
	for i, field := range fields {
		ok := false
		for _, part := range strings.Split(field, ",") {
			switch {
			case part == "*":
				ok = true
			case strings.HasPrefix(part, "*/"):
				n := atoi(part[2:])
				ok = ok || (n > 0 && values[i]%n == 0)
			default:
				ok = ok || atoi(part) == values[i]
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// pgCronMatches is cron's rule, written independently of the code under
// test: fields as sets (ranges, steps counted from the range's start,
// weekday 7 = Sunday), and the day test ORed unless a day field begins
// with "*".
func pgCronMatches(schedule string, at time.Time) bool {
	fields := strings.Fields(schedule)
	limits := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	in := func(i, v int) bool {
		lo, hi := limits[i][0], limits[i][1]
		for _, part := range strings.Split(fields[i], ",") {
			base, stepText, hasStep := strings.Cut(part, "/")
			step := 1
			if hasStep {
				step = atoi(stepText)
			}
			from, to := lo, hi
			if base != "*" {
				if a, b, isRange := strings.Cut(base, "-"); isRange {
					from, to = atoi(a), atoi(b)
				} else {
					from, to = atoi(base), atoi(base)
					if hasStep {
						to = hi
					}
				}
			} else if i == 4 {
				to = 6
			}
			for x := from; x <= to; x += step {
				y := x
				if i == 4 {
					y = x % 7
				}
				if y == v {
					return true
				}
			}
		}
		return false
	}
	if !in(0, at.Minute()) || !in(1, at.Hour()) || !in(3, int(at.Month())) {
		return false
	}
	dom, dow := in(2, at.Day()), in(4, int(at.Weekday()))
	if strings.HasPrefix(fields[2], "*") || strings.HasPrefix(fields[4], "*") {
		return dom && dow
	}
	return dom || dow
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
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
	for name, command := range map[string]string{
		// A text that looks like a qualified name, a call or a comment is data.
		"literal":      "INSERT INTO log (msg) VALUES ('ran public.job; net.http_post(x) -- ok')",
		"call":         "CALL build_report()",
		"cte":          "WITH old AS (DELETE FROM sessions WHERE expires_at < now() RETURNING id) INSERT INTO purged (id) SELECT id FROM old",
		"table alias":  "SELECT net.amount FROM invoices net WHERE net.paid",
		"table named":  "UPDATE storage SET used = 0 WHERE storage.used < 0",
		"alias method": "SELECT count(*) FROM orders o JOIN users u ON u.id = o.user_id WHERE lower(u.email) = 'x'",
	} {
		if got := planCronJob(pgCronJob{ID: 1, Name: "n", Schedule: "* * * * *", Command: command}, "UTC"); !got.Ready() || got.Request.Action != command {
			t.Errorf("%s: action=%q problems=%v", name, got.Request.Action, got.Problems)
		}
	}
	// A comment is not part of the job.
	if got := planCronJob(pgCronJob{ID: 1, Name: "n", Schedule: "* * * * *", Command: "-- nightly\nDELETE FROM t /* all */ WHERE old"}, "UTC"); !got.Ready() || strings.Contains(got.Request.Action, "nightly") {
		t.Errorf("comment: %q %v", got.Request.Action, got.Problems)
	}
	for name, tc := range map[string]struct{ command, want string }{
		"two statements":      {"DELETE FROM a; DELETE FROM b;", "2 statements"},
		"another schema":      {"DELETE FROM auth.sessions WHERE not_after < now()", `no "auth" schema`},
		"quoted schema":       {`DELETE FROM "auth"."sessions"`, `no "auth" schema`},
		"quoted pg_net":       {`select "net"."http_post"('https://x.test/y')`, "make an edge function"},
		"own schema function": {"select private.cleanup()", `the schema "private"`},
		"own schema table":    {"DELETE FROM archive.events WHERE at < now()", `the schema "archive"`},
		"http extension":      {"select http_post('https://x.test/hook', '{}', 'application/json')", "http extension"},
		"http extension bare": {"select status from http(('GET', 'https://x.test', NULL, NULL, NULL)::http_request)", "http extension"},
		"system catalog":      {"SELECT count(*) FROM pg_stat_activity", "can't run as a cron job"},
		"a DO block":          {"DO $$ BEGIN PERFORM 1; END $$", "a DO statement"},
		"unschedule call":     {"SELECT cron.unschedule('x')", `no "cron" schema`},
		"vacuum":              {"VACUUM ANALYZE events", "a VACUUM statement"},
		"truncate":            {"TRUNCATE sessions", "a TRUNCATE statement"},
		"refresh":             {"REFRESH MATERIALIZED VIEW daily_stats", "a REFRESH statement"},
		"alter":               {"ALTER TABLE t ADD COLUMN x int", "an ALTER statement"},
		"set":                 {"SET statement_timeout = 0", "a SET statement"},
		"empty":               {"  ;  ", "empty"},
		"only a comment":      {"-- nothing", "empty"},
	} {
		got := planCronJob(pgCronJob{ID: 1, Name: "j", Schedule: "* * * * *", Command: tc.command}, "UTC")
		want := strings.Replace(tc.want, "an ALTER", "a ALTER", 1)
		if got.Ready() || !strings.Contains(strings.Join(got.Problems, "\n"), want) {
			t.Errorf("%s: problems = %v, want one mentioning %q", name, got.Problems, want)
		}
	}
}

func TestPlanCronJob_EdgeFunctionCalls(t *testing.T) {
	const key = "eyJhbGciOiJIUzI1NiJ9.fakepayload.fakesignature"
	command := `
    -- every weekday morning
    select
      net.http_post(
          url:='https://abcdefgh.supabase.co/functions/v1/send-digest',
          headers:='{"Content-Type": "application/json", "Authorization": "Bearer ` + key + `", "x-api-key": "sb_secret_abc123456", "X-Source": "cron"}'::jsonb,
          body:='{"kind": "daily", "note": "it''s time", "headers := x": 1}'::jsonb
      ) as request_id;`
	p := planCronJob(pgCronJob{ID: 2, Name: "digest", Schedule: "0 7 * * 1-5", Command: command, Active: true}, "UTC")
	if !p.Ready() {
		t.Fatalf("problems: %v", p.Problems)
	}
	if p.Request.ActionType != "function" || p.Request.Action != "send-digest" || p.Request.Schedule != "0 7 * * 1,2,3,4,5" {
		t.Errorf("request = %+v", p.Request)
	}
	if string(p.Request.Payload) != `{"kind": "daily", "note": "it's time", "headers := x": 1}` {
		t.Errorf("payload = %s", p.Request.Payload)
	}
	// No header is carried into the job, whatever its name; the names are
	// listed so the developer knows what was there.
	reqJSON, _ := json.Marshal(p.Request)
	for _, secret := range []string{key, "sb_secret_abc123456", "x-api-key", "X-Source", "headers\":"} {
		if strings.Contains(string(reqJSON), secret) {
			t.Errorf("the job that would be created contains %q: %s", secret, reqJSON)
		}
	}
	notes := strings.Join(p.Notes, "\n")
	if !strings.Contains(notes, "aren't carried over (Authorization, Content-Type, X-Source, x-api-key)") {
		t.Errorf("notes = %v", p.Notes)
	}

	// Positional arguments: pg_net's order is url, body, params, headers.
	positional := planCronJob(pgCronJob{ID: 3, Name: "pos", Schedule: "* * * * *", Command: `select net.http_post('https://abcdefgh.supabase.co/functions/v1/sync', '{"a":1}'::jsonb, '{}'::jsonb, '{"Authorization":"Bearer ` + key + `","X-K":"v"}'::jsonb, 5000)`}, "UTC")
	if !positional.Ready() || positional.Request.Action != "sync" || string(positional.Request.Payload) != `{"a":1}` {
		t.Errorf("positional: %+v %v", positional.Request, positional.Problems)
	}
	if n := strings.Join(positional.Notes, "\n"); !strings.Contains(n, "aren't carried over (Authorization, X-K)") {
		t.Errorf("positional notes = %v", positional.Notes)
	}
	// http_get has no body: its second argument is the query parameters.
	get := planCronJob(pgCronJob{ID: 6, Name: "g", Schedule: "* * * * *", Command: `select net.http_get(url => 'https://x.supabase.co/functions/v1/warm-up')`}, "UTC")
	if !get.Ready() || get.Request.Action != "warm-up" || get.Request.Payload != nil || !strings.Contains(strings.Join(get.Notes, "\n"), "with GET") {
		t.Errorf("GET to a function: %+v %v %v", get.Request, get.Problems, get.Notes)
	}

	// A body or header built in SQL can't be read as a value.
	dynamic := planCronJob(pgCronJob{ID: 3, Name: "dyn", Schedule: "*/10 * * * *", Command: `select net.http_post(
		url := 'https://abcdefgh.supabase.co/functions/v1/sync_orders',
		headers := jsonb_build_object('Authorization', 'Bearer ' || (select decrypted_secret from vault.decrypted_secrets where name = 'service_key')),
		body := concat('{"time": "', now(), '"}')::jsonb)`}, "UTC")
	if !dynamic.Ready() || dynamic.Request.Action != "sync_orders" || dynamic.Request.Payload != nil {
		t.Errorf("dynamic: %+v %v", dynamic.Request, dynamic.Problems)
	}
	if n := strings.Join(dynamic.Notes, "\n"); !strings.Contains(n, "body was built in SQL") || !strings.Contains(n, "headers (built in SQL)") {
		t.Errorf("notes = %v", dynamic.Notes)
	}

	// A host that isn't Supabase's is pointed out.
	custom := planCronJob(pgCronJob{ID: 7, Name: "c", Schedule: "* * * * *", Command: `select net.http_post('https://api.example.org/functions/v1/x')`}, "UTC")
	if !custom.Ready() || !strings.Contains(strings.Join(custom.Notes, "\n"), "api.example.org") {
		t.Errorf("custom host: %v %v", custom.Problems, custom.Notes)
	}
}

// What can't become a function job is refused: it is never half-planned.
func TestPlanCronJob_HTTPCallsThatAreNotPlanned(t *testing.T) {
	const fn = "https://abcdefgh.supabase.co/functions/v1/"
	for name, tc := range map[string]struct{ command, want string }{
		"external URL":       {`select net.http_get('https://hooks.example.org/ping?key=sb_secret_abc123456')`, "make an edge function"},
		"path in the middle": {`select net.http_post('https://example.org/api/functions/v1/x')`, "make an edge function"},
		"dot in the name":    {`select net.http_post('` + fn + `my.fn')`, `function name "my.fn"`},
		"sub path":           {`select net.http_post('` + fn + `x/sub/path')`, "path below its name"},
		"query string":       {`select net.http_post('` + fn + `x?apikey=sb_secret_abc123456')`, "query string"},
		"built URL":          {`select net.http_post(current_setting('app.url') || '/functions/v1/x', '{"cb":"` + fn + `evil"}'::jsonb)`, "built in SQL"},
		"URL only in body":   {`select net.http_post(url := base_url(), body := '{"cb":"` + fn + `evil"}'::jsonb)`, "built in SQL"},
		// The rest of the command would be lost: these go the SQL way and
		// are refused there.
		"call and statement": {`select net.http_post('` + fn + `x'); DELETE FROM public.t`, "2 statements"},
		"call in a CTE":      {`WITH r AS (select net.http_post('` + fn + `x')) INSERT INTO public.log SELECT * FROM r`, `no "net" schema`},
		"call in a DO block": {`DO $$ BEGIN PERFORM net.http_post('` + fn + `x'); DELETE FROM t; END $$`, "a DO statement"},
		"call with a filter": {`select net.http_post('` + fn + `x') FROM users WHERE active`, `no "net" schema`},
	} {
		got := planCronJob(pgCronJob{ID: 1, Name: "j", Schedule: "* * * * *", Command: tc.command}, "UTC")
		problems := strings.Join(got.Problems, "\n")
		if got.Ready() || !strings.Contains(problems, tc.want) {
			t.Errorf("%s: ready=%v problems=%v, want one mentioning %q", name, got.Ready(), got.Problems, tc.want)
		}
		if got.Request.Action == "evil" {
			t.Errorf("%s: planned the function named in the body", name)
		}
		if strings.Contains(problems, "sb_secret_abc123456") {
			t.Errorf("%s: a key is in the message: %s", name, problems)
		}
	}
	// A commented-out call is a comment: the statement after it is the job.
	commented := planCronJob(pgCronJob{ID: 1, Name: "j", Schedule: "* * * * *", Command: "-- was: select net.http_post('" + fn + "old')\nDELETE FROM public.sessions WHERE expires_at < now()"}, "UTC")
	if !commented.Ready() || commented.Request.ActionType != "sql" || commented.Request.Action != "DELETE FROM sessions WHERE expires_at < now()" {
		t.Errorf("commented-out call: %+v %v", commented.Request, commented.Problems)
	}
}

func TestPrintCronPlan_ShowsNoSecrets(t *testing.T) {
	const key = "eyJhbGciOiJIUzI1NiJ9.fakepayload.fakesignature"
	jobs := []pgCronJob{
		{ID: 1, Name: "fn", Schedule: "* * * * *", Active: true, Command: `select net.http_post('https://a.supabase.co/functions/v1/x', '{"token":"sb_secret_abc123456","kind":"k"}'::jsonb, '{}'::jsonb, '{"Authorization":"Bearer ` + key + `"}'::jsonb)`},
		{ID: 2, Name: "ext", Schedule: "* * * * *", Active: true, Command: `select net.http_get('https://hooks.example.org/ping?key=sb_secret_abc123456')`},
		{ID: 3, Name: "sql", Schedule: "* * * * *", Active: true, Command: `SELECT notify('Bearer ` + key + `', 'https://x.test/a?token=sb_secret_abc123456')`},
		{ID: 4, Name: "sql", Schedule: "* * * * *", Active: true, Command: `SELECT 1`},
	}
	var out strings.Builder
	for _, p := range planCronJobs(jobs, "UTC") {
		printCronPlan(&out, p)
	}
	text := out.String()
	for _, secret := range []string{key, "sb_secret_abc123456", "fakesignature"} {
		if strings.Contains(text, secret) {
			t.Errorf("the printed plan shows %q:\n%s", secret, text)
		}
	}
	// The payload's keys are shown, not its values; and the second job
	// called "sql" has a name of its own.
	for _, want := range []string{"payload: {kind, token}", "(was https://a.supabase.co/functions/v1/x)", "sql-4", `created as "sql-4"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the printed plan lacks %q:\n%s", want, text)
		}
	}
}

func TestSplitArgumentsAndJSONLiteral(t *testing.T) {
	args := `url := 'https://x/functions/v1/a', body := '{"a": [1, 2], "b": "x, y", "headers := z": 1}'::jsonb, timeout_milliseconds := 5000, f(1, 2), "q,r" := ARRAY[1, 2]`
	got := splitArguments(args)
	want := []string{`url := 'https://x/functions/v1/a'`, `body := '{"a": [1, 2], "b": "x, y", "headers := z": 1}'::jsonb`, `timeout_milliseconds := 5000`, `f(1, 2)`, `"q,r" := ARRAY[1, 2]`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("splitArguments:\n%q\nwant:\n%q", got, want)
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

func TestCheckCronTimezone(t *testing.T) {
	for _, ok := range []string{"UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires", "Etc/GMT+2"} {
		if err := checkCronTimezone(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"+02", "UTC+2", "<+02>-02", "europe/berlin", "Local", "Not/AZone", ""} {
		if err := checkCronTimezone(bad); err == nil || !strings.Contains(err.Error(), "--timezone") {
			t.Errorf("%q: error %v, want the hint to pass --timezone", bad, err)
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
	for _, p := range planCronJobs(jobs, tz) {
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
