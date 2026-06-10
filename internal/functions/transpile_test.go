package functions

import (
	"strings"
	"testing"
)

// Issue #189: the docs promise TypeScript + Supabase-style `export
// default`, but the runner executes plain JS via new Function(). These
// tests pin the deploy-time transpile contract.

func TestTranspile_StripsTypeScript(t *testing.T) {
	src := `interface Listing { id: string }
const greet = (name: string): string => "hi " + name;
globalThis.handler = async (req: Request, ctx: any): Promise<Response> => {
  return new Response(greet("eurobase"));
};`
	out, err := Transpile(src)
	if err != nil {
		t.Fatalf("Transpile failed: %v", err)
	}
	for _, tsLeftover := range []string{"interface", ": string", ": Promise<Response>"} {
		if strings.Contains(out, tsLeftover) {
			t.Errorf("compiled output still contains TS syntax %q:\n%s", tsLeftover, out)
		}
	}
	if !strings.Contains(out, "globalThis.handler") {
		t.Errorf("compiled output lost the handler assignment:\n%s", out)
	}
}

func TestTranspile_ExportDefaultBecomesModuleExports(t *testing.T) {
	src := `export default async function handler(req: Request): Promise<Response> {
  return new Response("ok");
}`
	out, err := Transpile(src)
	if err != nil {
		t.Fatalf("Transpile failed: %v", err)
	}
	// esbuild's CommonJS output replaces module.exports with an object
	// whose .default is the handler — the shape the worker bootstrap's
	// detection suffix looks for.
	if !strings.Contains(out, "module.exports") {
		t.Errorf("expected CommonJS module.exports in output:\n%s", out)
	}
	if !strings.Contains(out, "default") {
		t.Errorf("expected a default export mapping in output:\n%s", out)
	}
}

func TestTranspile_PlainJSPassesThrough(t *testing.T) {
	src := `globalThis.handler = async (req, ctx) => {
  const rows = await ctx.db.sql("SELECT 1");
  return new Response(JSON.stringify(rows));
};`
	out, err := Transpile(src)
	if err != nil {
		t.Fatalf("Transpile failed on plain JS: %v", err)
	}
	if !strings.Contains(out, "globalThis.handler") {
		t.Errorf("plain JS handler assignment lost:\n%s", out)
	}
}

func TestTranspile_SyntaxErrorHasLineInfo(t *testing.T) {
	src := "globalThis.handler = async (req => {\n  return new Response('x');\n};"
	_, err := Transpile(src)
	if err == nil {
		t.Fatal("expected a compile error for broken syntax")
	}
	if !strings.Contains(err.Error(), "line ") {
		t.Errorf("expected line info in error, got: %v", err)
	}
}

func TestTranspile_URLImportCompilesToRequire(t *testing.T) {
	// Third-party imports aren't supported — they compile to require()
	// calls, which the worker bootstrap's require stub rejects at load
	// time with a clear message. Pin that the compiled shape is indeed
	// a require call so the stub stays the right backstop.
	src := `import { serve } from "https://deno.land/std/http/server.ts";
serve(() => new Response("hi"));`
	out, err := Transpile(src)
	if err != nil {
		t.Fatalf("Transpile failed: %v", err)
	}
	if !strings.Contains(out, `require("https://deno.land/std/http/server.ts")`) {
		t.Errorf("expected URL import to compile to a require() call:\n%s", out)
	}
}
