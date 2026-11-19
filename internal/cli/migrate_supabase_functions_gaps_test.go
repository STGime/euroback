package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// Code that would run on Supabase but can't work, or can't be safe, on
// Eurobase is pointed out before a deploy.
func TestFindCompatGaps_ServiceKeyAndDirectAPIUse(t *testing.T) {
	src := strings.Join([]string{
		`const admin = createClient(Deno.env.get("SUPABASE_URL")!, Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")!)`, // 1: fine
		`const key = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") ?? ""`,                                           // 2: fine
		`if (req.headers.get("x-cron-key") !== Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")) return deny()`,        // 3
		"if (auth === `Bearer ${Deno.env.get('SUPABASE_SERVICE_ROLE_KEY')}`) trusted = true",                    // 4
		"await fetch(`${SUPABASE_URL}/rest/v1/orders`, { headers: { apikey: anon } })",                          // 5
		`const res = await fetch(Deno.env.get("SUPABASE_URL") + "/functions/v1/send-mail")`,                     // 6
		`const { data } = await sb.auth.getClaims(token)`,                                                       // 7
		`EdgeRuntime.waitUntil(sendMail())`,                                                                     // 8
		`const { data: u } = await sb.auth.getUser(token)`,                                                      // 9: fine
		`const path = "/api/v1/things"`,                                                                         // 10: fine
	}, "\n")
	var got []string
	for _, f := range findCompatGaps("f/index.ts", src) {
		got = append(got, strings.TrimPrefix(f.File, "f/")+":"+string(rune('0'+f.Line))+" "+f.What)
	}
	want := []string{
		"index.ts:3 the service role key compared with or sent in a request",
		"index.ts:4 the service role key compared with or sent in a request",
		"index.ts:5 a direct call to the Supabase API",
		"index.ts:6 a direct call to the Supabase API",
		"index.ts:7 an auth call the compatibility layer doesn't have",
		"index.ts:8 EdgeRuntime.waitUntil()",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A function open to calls without a signed-in user that makes an
// anon-key client is told what that client will do.
func TestAnalyze_NotesAnonClientInAnOpenFunction(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"functions/public/index.ts": "import { createClient } from 'npm:@supabase/supabase-js@2'\n" +
			"Deno.serve(async () => {\n  const sb = createClient(Deno.env.get('SUPABASE_URL')!, Deno.env.get('SUPABASE_ANON_KEY')!)\n" +
			"  return new Response(JSON.stringify(await sb.from('posts').select('*')))\n})\n",
	})
	entry := filepath.Join(dir, "functions/public/index.ts")
	open := &supabaseFunction{Name: "public", Dir: filepath.Dir(entry), Entry: entry, VerifyJWT: false}
	open.analyze()
	if len(open.Notes) != 1 || !strings.Contains(open.Notes[0], "refused when nobody is signed in") || !open.Ready() {
		t.Errorf("open function: notes=%v ready=%v, want one note and still ready", open.Notes, open.Ready())
	}
	closed := &supabaseFunction{Name: "public", Dir: filepath.Dir(entry), Entry: entry, VerifyJWT: true}
	closed.analyze()
	if len(closed.Notes) != 0 {
		t.Errorf("a function that requires sign-in needs no note: %v", closed.Notes)
	}
}

// The wrapper never ships a fixed service role key: the value is made per
// request, and a project's own value is looked up when the code uses it.
func TestSupabaseCompatWrapper_NoFixedServiceKey(t *testing.T) {
	banner, footer := supabaseCompatWrapper([]string{"STRIPE_KEY", "SUPABASE_SERVICE_ROLE_KEY"})
	if strings.Contains(banner+footer, `"eurobase-service-role"`) {
		t.Error("the wrapper contains a fixed service role key")
	}
	for _, want := range []string{"crypto.randomUUID()", "serviceKey: __env.SUPABASE_SERVICE_ROLE_KEY", `["STRIPE_KEY","SUPABASE_SERVICE_ROLE_KEY"]`} {
		if !strings.Contains(banner, want) {
			t.Errorf("wrapper lacks %q", want)
		}
	}
	if strings.Contains(supabaseCompatSource, `"eurobase-service-role"`) {
		t.Error("the client module contains a fixed service role key")
	}
}

// console.* in a function built with the compatibility layer goes to the
// function's log, one bounded line per call.
func TestSupabaseCompatWrapper_ConsoleLines(t *testing.T) {
	banner, _ := supabaseCompatWrapper(nil)
	for _, want := range []string{"const console = new Proxy(globalThis.console", "line.length > 2048", `if (a === undefined) return "undefined"`} {
		if !strings.Contains(banner, want) {
			t.Errorf("wrapper lacks %q", want)
		}
	}
}
