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
	// the file parses.
	if strings.Count(res.Source, "});") > strings.Count(res.Source, "})") {
		t.Errorf("stray trailing `)` survived:\n%s", res.Source)
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
