package cli

import (
	"strings"
	"testing"
)

// #272: pure translator tests. The rewrites are best-effort text
// transformations — a broken rewrite silently emits code that fails
// at deploy time, so tests pin every rule.

// ── Deno.env.get rewrites ────────────────────────────────────────────

func TestTranslateFunction_DenoEnvGet_SingleQuotes(t *testing.T) {
	in := `const x = Deno.env.get('STRIPE_KEY');`
	res := TranslateFunction(in)
	if !strings.Contains(res.Source, "ctx.env.STRIPE_KEY") {
		t.Errorf("Deno.env.get('STRIPE_KEY') → ctx.env.STRIPE_KEY failed:\n%s", res.Source)
	}
	if strings.Contains(res.Source, "Deno.env.get") {
		t.Errorf("Deno.env.get survived:\n%s", res.Source)
	}
}

func TestTranslateFunction_DenoEnvGet_DoubleQuotes(t *testing.T) {
	in := `const x = Deno.env.get("SLACK_URL");`
	res := TranslateFunction(in)
	if !strings.Contains(res.Source, "ctx.env.SLACK_URL") {
		t.Errorf("double-quoted env key not rewritten:\n%s", res.Source)
	}
}

func TestTranslateFunction_DenoEnvGet_DynamicWarns(t *testing.T) {
	in := `const key = 'DYNAMIC'; const val = Deno.env.get(key);`
	res := TranslateFunction(in)
	// Dynamic form must NOT be rewritten (would change semantics).
	if !strings.Contains(res.Source, "Deno.env.get(key)") {
		t.Errorf("dynamic Deno.env.get was silently rewritten — semantics would change:\n%s", res.Source)
	}
	// Must warn.
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w.note, "dynamic") || strings.Contains(w.note, "non-literal") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dynamic-access warning; got %+v", res.Warnings)
	}
}

// SUPABASE_URL etc. get rewritten AND warned — the syntax is fine, but
// the semantic mapping isn't guaranteed. Tenant needs to eyeball.
func TestTranslateFunction_SupabaseEnvVarWarns(t *testing.T) {
	for _, key := range []string{
		"SUPABASE_URL", "SUPABASE_ANON_KEY", "SUPABASE_SERVICE_ROLE_KEY",
		"SUPABASE_DB_URL", "SUPABASE_JWT_SECRET",
	} {
		in := "const x = Deno.env.get('" + key + "');"
		res := TranslateFunction(in)
		// Still rewrites the syntax.
		if !strings.Contains(res.Source, "ctx.env."+key) {
			t.Errorf("supabase env %s: syntax not rewritten:\n%s", key, res.Source)
		}
		// Must have warned.
		found := false
		for _, w := range res.Warnings {
			if strings.Contains(w.note, key) {
				found = true
			}
		}
		if !found {
			t.Errorf("supabase env %s: no warning emitted; got %+v", key, res.Warnings)
		}
	}
}

// ── Deno.serve rewrites ──────────────────────────────────────────────

func TestTranslateFunction_DenoServeArrowAsync(t *testing.T) {
	in := `Deno.serve(async (req) => {
  return new Response("ok");
});`
	res := TranslateFunction(in)
	if !strings.Contains(res.Source, "module.exports = async (req, ctx) =>") {
		t.Errorf("Deno.serve(async …) → module.exports failed:\n%s", res.Source)
	}
	if strings.Contains(res.Source, "Deno.serve") {
		t.Errorf("Deno.serve survived:\n%s", res.Source)
	}
	// Trailing `)` from the Deno.serve wrapper must be stripped so
	// the file parses. Positive assertion: the emitted code ends
	// with `});` (the tenant's own closing brace + semicolon), NOT
	// `}));` (which would mean the Deno.serve closer leaked
	// through). The previous version of this test compared
	// `Count("});")` against `Count("})")` — mathematically
	// impossible for the former to exceed the latter, so it fired
	// never. (#277 review M #7 — tautology fix.)
	trimmed := strings.TrimSpace(res.Source)
	if strings.HasSuffix(trimmed, "}));") || strings.HasSuffix(trimmed, "}))") {
		t.Errorf("stray trailing `)` survived at end of file:\n%s", res.Source)
	}
	// The walker consumed the whole `Deno.serve(...)` including its
	// closing `)`, so the emitted code ends with `};` — the `}`
	// closes the arrow body, `;` is the original statement
	// terminator from just after `Deno.serve(...)`.
	if !strings.HasSuffix(trimmed, "};") {
		t.Errorf("expected the rewritten file to end with `};`, got:\n%s", res.Source)
	}
}

func TestTranslateFunction_DenoServePlainArrow(t *testing.T) {
	in := `Deno.serve((req) => new Response("hi"));`
	res := TranslateFunction(in)
	if !strings.Contains(res.Source, "module.exports = async (req, ctx) =>") {
		t.Errorf("Deno.serve((req) => …) → module.exports failed:\n%s", res.Source)
	}
}

func TestTranslateFunction_DenoServeTypedRequest(t *testing.T) {
	// TypeScript projects often write `(req: Request) => …` — the
	// regex tolerates the type annotation.
	in := `Deno.serve(async (req: Request) => new Response("ok"));`
	res := TranslateFunction(in)
	if !strings.Contains(res.Source, "module.exports = async (req, ctx) =>") {
		t.Errorf("typed request arg not handled:\n%s", res.Source)
	}
}

func TestTranslateFunction_DenoServeNamedHandler(t *testing.T) {
	in := `async function handler(req) { return new Response("hi"); }
Deno.serve(handler);`
	res := TranslateFunction(in)
	if !strings.Contains(res.Source, "module.exports = handler") {
		t.Errorf("named-handler form not rewritten:\n%s", res.Source)
	}
	if strings.Contains(res.Source, "Deno.serve") {
		t.Errorf("Deno.serve survived:\n%s", res.Source)
	}
}

// ── unsupported: in-handler Supabase SDK client ─────────────────────

func TestTranslateFunction_UnsupportedSDKClient_ExplicitUrl(t *testing.T) {
	in := `import { createClient } from 'https://esm.sh/@supabase/supabase-js@2';
const supabase = createClient(SUPABASE_URL, SUPABASE_SERVICE_ROLE_KEY);
Deno.serve(async (req) => {
  const { data } = await supabase.from('foo').select();
  return Response.json(data);
});`
	res := TranslateFunction(in)
	if !res.Unsupported {
		t.Errorf("expected Unsupported=true on createClient use")
	}
	if !strings.Contains(res.UnsupportedReason, "ctx.db.sql") {
		t.Errorf("UnsupportedReason should point at ctx.db.sql; got %q", res.UnsupportedReason)
	}
	// Must NOT emit half-translated code.
	if res.Source != "" {
		t.Errorf("Source should be empty on unsupported — got %q", res.Source)
	}
}

func TestTranslateFunction_UnsupportedSDKClient_EnvGet(t *testing.T) {
	// Sometimes the URL is read from env inside the createClient call.
	in := `const supabase = createClient(Deno.env.get('SUPABASE_URL'), Deno.env.get('SUPABASE_ANON_KEY'));`
	res := TranslateFunction(in)
	if !res.Unsupported {
		t.Errorf("expected Unsupported on createClient with Deno.env.get URL")
	}
}

func TestTranslateFunction_UnsupportedSDKClient_LowercaseVar(t *testing.T) {
	in := `const supabaseUrl = "https://x.supabase.co";
const supabase = createClient(supabaseUrl, key);`
	res := TranslateFunction(in)
	if !res.Unsupported {
		t.Errorf("expected Unsupported on createClient with lowercase supabaseUrl var")
	}
}

// ── https:// import warnings ─────────────────────────────────────────

func TestTranslateFunction_HttpsImportWarns(t *testing.T) {
	in := `import { z } from 'https://esm.sh/zod@3.22.4';
console.log("no handler");`
	res := TranslateFunction(in)
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w.note, "https://") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected https:// import warning; got %+v", res.Warnings)
	}
}

func TestTranslateFunction_LocalImportsDontWarn(t *testing.T) {
	// Relative imports (`./`, `../`) and bare npm imports are fine.
	in := `import { helper } from './helper.ts';
import { z } from 'zod';`
	res := TranslateFunction(in)
	for _, w := range res.Warnings {
		if strings.Contains(w.note, "https://") {
			t.Errorf("false-positive https:// warning on local imports: %q", w.note)
		}
	}
}

// ── end-to-end sample ────────────────────────────────────────────────

// ── #277 review round-1 pins ────────────────────────────────────────

// TestTranslateFunction_SingleExpressionArrowNoStrayParen pins H #3:
// a body without `{}` still had a stray `)` from the Deno.serve wrapper.
// New walker-based rewrite handles balanced parens directly so single-
// expression arrows come out clean.
func TestTranslateFunction_SingleExpressionArrowNoStrayParen(t *testing.T) {
	in := `Deno.serve((req) => new Response("hi"));`
	res := TranslateFunction(in)
	if !strings.Contains(res.Source, "module.exports = async (req, ctx) => new Response(") {
		t.Errorf("single-expression arrow not rewritten:\n%s", res.Source)
	}
	trimmed := strings.TrimSpace(res.Source)
	// Must end with `);` — the ONE `)` for the Response(...) call
	// followed by the statement `;`. NOT `));` which would mean the
	// Deno.serve closer leaked through.
	if strings.HasSuffix(trimmed, "));") {
		t.Errorf("stray trailing `)` on single-expression arrow:\n%s", res.Source)
	}
}

// TestTranslateFunction_MultipleDenoServe pins H #1: two Deno.serve
// calls in one file must BOTH get rewritten (the old end-anchored
// cleanup regex only stripped the last).
func TestTranslateFunction_MultipleDenoServe(t *testing.T) {
	in := `if (dev) {
  Deno.serve(async (req) => { return new Response("dev"); });
} else {
  Deno.serve(async (req) => { return new Response("prod"); });
}`
	res := TranslateFunction(in)
	if strings.Contains(res.Source, "Deno.serve") {
		t.Errorf("one of the two Deno.serve calls survived:\n%s", res.Source)
	}
	// Both bodies preserved.
	if !strings.Contains(res.Source, `"dev"`) || !strings.Contains(res.Source, `"prod"`) {
		t.Errorf("body content lost across the two rewrites:\n%s", res.Source)
	}
	// Two rewrites tallied.
	total := 0
	for _, n := range res.Rewrites {
		total += n
	}
	if total < 2 {
		t.Errorf("expected ≥2 rewrites for two Deno.serve calls, got %d (%v)", total, res.Rewrites)
	}
}

// TestTranslateFunction_DenoServeOptionsArgWarns pins H #2: a second
// (options) arg used to leave `, { port: 8000 })` dangling silently.
// The walker now detects it and warns.
func TestTranslateFunction_DenoServeOptionsArgWarns(t *testing.T) {
	in := `Deno.serve(async (req) => new Response("hi"), { port: 8000 });`
	res := TranslateFunction(in)
	if !strings.Contains(res.Source, "module.exports = async (req, ctx) => new Response(") {
		t.Errorf("arrow not rewritten in presence of options arg:\n%s", res.Source)
	}
	if strings.Contains(res.Source, "{ port: 8000 }") {
		t.Errorf("options arg leaked into the rewritten source:\n%s", res.Source)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w.note, "options") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning about the dropped options arg; got %+v", res.Warnings)
	}
}

// TestTranslateFunction_AliasedSDKImportWarns pins H #4: even if the
// SDK is imported under an alias (`createClient as sb`), we warn
// because there's no in-handler SDK on Eurobase.
func TestTranslateFunction_AliasedSDKImportWarns(t *testing.T) {
	in := `import { createClient as sb } from '@supabase/supabase-js';
Deno.serve(async (req) => {
  const client = sb('https://x.supabase.co', 'key');
  return Response.json({ ok: true });
});`
	res := TranslateFunction(in)
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w.note, "@supabase/supabase-js") || strings.Contains(w.note, "SDK") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a Supabase-SDK-import warning; got %+v", res.Warnings)
	}
}

// TestTranslateFunction_NonReqArgWarnsAndLeavesAlone pins M #6:
// a handler with a differently-named arg (`request`, `event`, `_`)
// isn't rewritten silently — Deno.serve survives, and a warning
// tells the tenant to hand-rewrite.
func TestTranslateFunction_NonReqArgWarnsAndLeavesAlone(t *testing.T) {
	in := `Deno.serve(async (request) => new Response("hi"));`
	res := TranslateFunction(in)
	// Deno.serve SURVIVES because the walker only rewrites recognised
	// shapes. That's the loud-fail signal.
	if !strings.Contains(res.Source, "Deno.serve(") {
		t.Errorf("unrecognised-arg Deno.serve was silently rewritten (bad):\n%s", res.Source)
	}
	// Warning must fire.
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w.note, "Deno.serve") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning about surviving Deno.serve; got %+v", res.Warnings)
	}
}

// TestTranslateFunction_NoDenoServeIsClean confirms a file without
// Deno.serve is a clean no-op — no false-positive rewrites, no
// surviving-Deno.serve warning.
func TestTranslateFunction_NoDenoServeIsClean(t *testing.T) {
	in := `export function util(a: number, b: number) { return a + b; }`
	res := TranslateFunction(in)
	if res.Source != in {
		t.Errorf("no-Deno.serve file was modified:\nwant: %s\ngot:  %s", in, res.Source)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w.note, "Deno.serve") {
			t.Errorf("false-positive Deno.serve warning: %+v", w)
		}
	}
}

func TestTranslateFunction_RealisticSupabaseFunction(t *testing.T) {
	in := `import { serve } from 'https://deno.land/std@0.168.0/http/server.ts';

Deno.serve(async (req) => {
  const name = Deno.env.get('APP_NAME') ?? 'world';
  const url = new URL(req.url);
  return Response.json({ hello: name, path: url.pathname });
});`
	res := TranslateFunction(in)
	// Rewrites fired.
	if !strings.Contains(res.Source, "module.exports = async (req, ctx) =>") {
		t.Errorf("Deno.serve rewrite missed:\n%s", res.Source)
	}
	if !strings.Contains(res.Source, "ctx.env.APP_NAME") {
		t.Errorf("env rewrite missed:\n%s", res.Source)
	}
	// Warning on the deno.land import.
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w.note, "https://") {
			found = true
		}
	}
	if !found {
		t.Errorf("no https:// import warning; got %+v", res.Warnings)
	}
	// Not unsupported.
	if res.Unsupported {
		t.Errorf("realistic function marked unsupported: %s", res.UnsupportedReason)
	}
}
