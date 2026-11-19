package cli

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/functions"
)

func TestListSupabaseFunctions_ConfigAndEntrypoints(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"supabase/config.toml":                       "[api]\nport = 54321\n\n[functions.stripe-webhook]\nverify_jwt = false\n\n[functions.\"quoted\"]\n# verify_jwt = false\nverify_jwt = true\n\n[db]\nverify_jwt = false\n",
		"supabase/functions/_shared/cors.ts":         "export const cors = {}\n",
		"supabase/functions/stripe-webhook/index.ts": "Deno.serve(() => new Response('ok'))\n",
		"supabase/functions/quoted/index.ts":         "Deno.serve(() => new Response('ok'))\n",
		"supabase/functions/custom/deno.json":        `{ "entrypoint": "src/main.ts", }`,
		"supabase/functions/custom/src/main.ts":      "Deno.serve(() => new Response('ok'))\n",
		"supabase/functions/escape/deno.json":        `{ "main": "../../../etc/passwd" }`,
		"supabase/functions/empty/readme.md":         "nothing here\n",
	})
	fns, err := listSupabaseFunctions(filepath.Join(dir, "supabase/functions"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*supabaseFunction{}
	for _, fn := range fns {
		got[fn.Name] = fn
	}
	if len(fns) != 5 || got["_shared"] != nil {
		t.Fatalf("functions = %d, want 5 without _shared", len(fns))
	}
	if got["stripe-webhook"].VerifyJWT || !got["quoted"].VerifyJWT || !got["custom"].VerifyJWT {
		t.Errorf("verify_jwt: webhook=%v quoted=%v custom=%v, want false / true / true", got["stripe-webhook"].VerifyJWT, got["quoted"].VerifyJWT, got["custom"].VerifyJWT)
	}
	if !strings.HasSuffix(filepath.ToSlash(got["custom"].Entry), "custom/src/main.ts") {
		t.Errorf("custom entry = %q", got["custom"].Entry)
	}
	// An entry point that leaves the function's folder is not followed.
	if got["escape"].Entry != "" || got["empty"].Entry != "" || got["empty"].Ready() {
		t.Errorf("escape entry = %q, empty entry = %q", got["escape"].Entry, got["empty"].Entry)
	}
}

func TestAnalyze_FindsGapsInTheFunctionAndItsSharedFiles(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"functions/_shared/db.ts": "import { createClient } from 'npm:@supabase/supabase-js@2'\n" +
			"export const admin = () => createClient(Deno.env.get('SUPABASE_URL')!, Deno.env.get('SUPABASE_SERVICE_ROLE_KEY')!)\n" +
			"export async function team(c: any) {\n  return await c.from('teams').select('*, members(*)')\n}\n",
		"functions/report/index.ts": "import { admin, team } from '../_shared/db.ts'\n" +
			"// supabase.functions.invoke('x') in a comment is not a finding\n" +
			"/* nor is .channel('y')\n   in a block comment */\n" +
			"Deno.serve(async (req) => {\n" +
			"  const sb = admin()\n" +
			"  const key = Deno.env.get(\"RESEND_API_KEY\")\n" +
			"  await sb.functions.invoke('send-mail', { body: {} })\n" +
			"  const { data } = await sb.auth.admin.listUsers()\n" +
			"  const ok = await sb.auth.admin.getUserById('x')\n" +
			"  const list = ['a'].includes('a')\n" +
			"  const file = await sb.storage.from('docs').download('a.pdf')\n" +
			"  return new Response(JSON.stringify(await team(sb)))\n})\n",
	})
	fn := &supabaseFunction{Name: "report", Dir: filepath.Join(dir, "functions/report"), Entry: filepath.Join(dir, "functions/report/index.ts"), VerifyJWT: true}
	fn.analyze()
	var got []string
	for _, f := range fn.Findings {
		got = append(got, f.File+":"+strconv.Itoa(f.Line)+" "+f.What)
	}
	want := []string{
		"report/index.ts:8 functions.invoke()",
		"report/index.ts:9 an auth.admin call",
		"report/index.ts:12 a storage call the compatibility layer doesn't have",
		"_shared/db.ts:4 select() with an embedded table",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !fn.UsesServiceRole || strings.Join(fn.Secrets, ",") != "RESEND_API_KEY" || fn.Ready() {
		t.Errorf("service=%v secrets=%v ready=%v, want true / RESEND_API_KEY / false", fn.UsesServiceRole, fn.Secrets, fn.Ready())
	}
}

// The whole command without --deploy: a function written the Supabase way
// builds and is reported ready; one with a gap is not.
func TestImportSupabaseFunctions_Report(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"supabase/config.toml":               "[functions.hook]\nverify_jwt = false\n",
		"supabase/functions/_shared/cors.ts": "export const corsHeaders = { 'Access-Control-Allow-Origin': '*' }\n",
		"supabase/functions/check/index.ts": "import { serve } from 'https://deno.land/std@0.190.0/http/server.ts'\n" +
			"import { createClient } from 'https://esm.sh/@supabase/supabase-js@2.45.0'\n" +
			"import { corsHeaders } from '../_shared/cors.ts'\n" +
			"serve(async (req) => {\n" +
			"  const sb = createClient(Deno.env.get('SUPABASE_URL') ?? '', Deno.env.get('SUPABASE_SERVICE_ROLE_KEY') ?? '')\n" +
			"  const { data } = await sb.auth.getUser(req.headers.get('Authorization')!.replace('Bearer ', ''))\n" +
			"  const secret = Deno.env.get('APPLE_SHARED_SECRET')\n" +
			"  return new Response(JSON.stringify({ id: data.user?.id, secret }), { headers: corsHeaders })\n})\n",
		"supabase/functions/hook/index.ts":   "Deno.serve(() => new Response('ok'))\n",
		"supabase/functions/notify/index.ts": "import { createClient } from 'https://esm.sh/@supabase/supabase-js@2'\nconst sb = createClient('u', 'k')\nDeno.serve(() => { sb.channel('room'); return new Response('ok') })\n",
	})
	cmd := importSupabaseFunctionsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--input", filepath.Join(dir, "supabase/functions")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"check", "✓ ready", "service role", "reads APPLE_SHARED_SECRET",
		"no sign-in required",
		"✗ needs changes", "notify/index.ts:3  a realtime channel",
		"3 function(s): 2 ready, 1 need changes",
		"eurobase vault set NAME VALUE", "APPLE_SHARED_SECRET",
		"--deploy",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestBundleFunction_SupabaseCompat(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"functions/pay/index.ts": "import { serve } from 'https://deno.land/std@0.168.0/http/server.ts'\n" +
			"import { createClient } from 'https://esm.sh/@supabase/supabase-js@2?target=deno'\n" +
			"const KEY = Deno.env.get('PAYMENT_KEY')\n" +
			"serve(async (req) => {\n" +
			"  const sb = createClient(Deno.env.get('SUPABASE_URL')!, Deno.env.get('SUPABASE_ANON_KEY')!)\n" +
			"  const { data } = await sb.from('orders').select('id').eq('status', process.env.ORDER_STATUS)\n" +
			"  return new Response(JSON.stringify({ data, KEY }))\n})\n",
	})
	entry := filepath.Join(dir, "functions/pay/index.ts")
	// Neither import is fetched: both are replaced by the CLI's modules.
	res, err := bundleFunction(entry, bundleOptions{Compat: compatSupabase})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compat != compatSupabase || len(res.Remote) != 0 || res.UsesServiceRole {
		t.Errorf("compat=%q remote=%v service=%v", res.Compat, res.Remote, res.UsesServiceRole)
	}
	if strings.Join(res.EnvNames, ",") != "ORDER_STATUS,PAYMENT_KEY" {
		t.Errorf("env names = %v", res.EnvNames)
	}
	for _, want := range []string{
		"globalThis.handler = async (__req, __ctx) =>", // the wrapper
		`["ORDER_STATUS","PAYMENT_KEY"]`,               // looked up in the Vault
		"json_populate_recordset",                      // the client module is in
		"eurobase-verified-user",
	} {
		if !strings.Contains(res.Code, want) {
			t.Errorf("bundle lacks %q", want)
		}
	}
	if strings.Contains(res.Code, "esm.sh") {
		t.Error("the Supabase client was bundled from the network instead of being replaced")
	}
	if _, err := functions.Transpile(res.Code); err != nil {
		t.Fatalf("the server would refuse the bundle: %v", err)
	}
	// Without compat the same file is an ordinary function with imports.
	if !looksLikeSupabaseFunction("Deno.serve(h)") || !looksLikeSupabaseFunction("import {createClient} from 'npm:@supabase/supabase-js@2'") ||
		looksLikeSupabaseFunction("export default async (req, ctx) => new Response('x')") {
		t.Error("looksLikeSupabaseFunction misjudged")
	}
}

func TestSupabaseCompatAlias(t *testing.T) {
	for spec, want := range map[string]string{
		"https://esm.sh/@supabase/supabase-js@2":          compatClientModule,
		"https://esm.sh/@supabase/supabase-js@2.45.0?dts": compatClientModule,
		"npm:@supabase/supabase-js@2":                     compatClientModule,
		"jsr:@supabase/supabase-js@2":                     compatClientModule,
		"@supabase/supabase-js":                           compatClientModule,
		"https://deno.land/std@0.168.0/http/server.ts":    compatServeModule,
		"https://deno.land/std/http/server.ts":            compatServeModule,
		"https://esm.sh/@supabase/ssr":                    "",
		"https://esm.sh/@supabase/supabase-js-helpers":    "",
		"https://deno.land/std@0.168.0/http/cookie.ts":    "",
		"npm:stripe@14":                                   "",
	} {
		if got := supabaseCompatAlias(spec); got != want {
			t.Errorf("supabaseCompatAlias(%q) = %q, want %q", spec, got, want)
		}
	}
}
