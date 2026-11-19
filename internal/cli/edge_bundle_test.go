package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/functions"
)

// writeTree writes files (relative path → content) under a temp dir and
// returns the dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBundleFunction_NoImportsIsDeployedAsWritten(t *testing.T) {
	src := "import type { EdgeHandler } from '@eurobase/sdk/functions'\n" +
		"const handler: EdgeHandler = async (req, ctx) => Response.json({ ok: true })\n" +
		"export default handler\n"
	dir := writeTree(t, map[string]string{"hello/index.ts": src})
	res, err := bundleFunction(filepath.Join(dir, "hello/index.ts"), bundleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Bundled || res.Code != src {
		t.Errorf("a function without imports must be deployed unchanged; bundled=%v\n%s", res.Bundled, res.Code)
	}
}

func TestBundleFunction_LocalAndSharedModules(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"functions/_shared/cors.ts": "export const corsHeaders: Record<string, string> = { 'Access-Control-Allow-Origin': '*' }\n",
		"functions/pay/util.ts":     "export function cents(n: number): number { return Math.round(n * 100) }\n",
		"functions/pay/index.ts": "import { corsHeaders } from '../_shared/cors.ts'\n" +
			"import { cents } from './util.ts'\n" +
			"export default async (req: Request) => new Response(String(cents(1.5)), { headers: corsHeaders })\n",
	})
	res, err := bundleFunction(filepath.Join(dir, "functions/pay/index.ts"), bundleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Bundled || len(res.Local) != 2 || len(res.Remote) != 0 {
		t.Fatalf("bundled=%v local=%v remote=%v, want two local modules", res.Bundled, res.Local, res.Remote)
	}
	for _, want := range []string{"Access-Control-Allow-Origin", "Math.round", "// Bundled by the eurobase CLI"} {
		if !strings.Contains(res.Code, want) {
			t.Errorf("bundle lacks %q", want)
		}
	}
	// The server compiles what the CLI sends; the result must have no
	// require() left, since the runner has no module loader.
	compiled, err := functions.Transpile(res.Code)
	if err != nil {
		t.Fatalf("the server would refuse the bundle: %v", err)
	}
	if strings.Contains(compiled, "require(") {
		t.Errorf("an import survived bundling:\n%s", compiled)
	}
	if !strings.Contains(compiled, "module.exports") {
		t.Errorf("the bundle doesn't export the handler the way the runner looks for it:\n%s", compiled)
	}
}

func TestBundleFunction_RemoteNpmAndImportMap(t *testing.T) {
	var agents []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.Header.Get("User-Agent"))
		switch r.URL.Path {
		case "/lib/mod.ts":
			w.Header().Set("Content-Type", "application/typescript")
			_, _ = w.Write([]byte("import { helper } from './helper.ts'\nimport { deep } from '/abs/deep.js'\nexport const greet = (n: string): string => helper(n) + deep\n"))
		case "/lib/helper.ts":
			_, _ = w.Write([]byte("export const helper = (n: string): string => 'hello ' + n\n"))
		case "/abs/deep.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte("export const deep = '!'\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := writeTree(t, map[string]string{
		"functions/deno.jsonc":   "{\n  // shared by every function\n  \"imports\": { \"greeter\": \"" + srv.URL + "/lib/mod.ts\", \"@shared/\": \"./_shared/\" }\n}\n",
		"functions/_shared/x.ts": "export const x = 'shared-x'\n",
		"functions/hi/index.ts": "import { greet } from 'greeter'\nimport { x } from '@shared/x.ts'\n" +
			"export default async () => new Response(greet('you') + x)\n",
	})
	res, err := bundleFunction(filepath.Join(dir, "functions/hi/index.ts"), bundleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Remote) != 3 || len(res.Local) != 1 {
		t.Errorf("remote=%v local=%v, want 3 fetched modules and 1 local", res.Remote, res.Local)
	}
	for _, want := range []string{"hello ", "shared-x", `"!"`} {
		if !strings.Contains(res.Code, want) {
			t.Errorf("bundle lacks %s:\n%s", want, res.Code)
		}
	}
	for _, a := range agents {
		if a != "eurobase-cli" {
			t.Errorf("User-Agent %q: must be the plain client name", a)
		}
	}
}

func TestBundleFunction_Refusals(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"node built-in": {"import fs from 'node:fs'\nexport default async () => new Response(String(fs))\n", "Node.js built-in"},
		"bare name":     {"import x from 'left-pad'\nexport default async () => new Response(String(x))\n", `npm:left-pad`},
		"missing local": {"import { a } from './nope.ts'\nexport default async () => new Response(a)\n", "nope.ts"},
		"syntax error":  {"export default async () => {\n", "index.ts"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeTree(t, map[string]string{"f/index.ts": tc.src})
			_, err := bundleFunction(filepath.Join(dir, "f/index.ts"), bundleOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestBundleFunction_VirtualModule(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"f/index.ts": "import { answer } from 'eurobase:test'\nexport default async () => new Response(String(answer))\n",
	})
	res, err := bundleFunction(filepath.Join(dir, "f/index.ts"), bundleOptions{
		Virtual: map[string]string{"eurobase:test": "export const answer: number = 42\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Bundled || !strings.Contains(res.Code, "42") {
		t.Errorf("virtual module not bundled:\n%s", res.Code)
	}
}

func TestBundleFunction_TooLarge(t *testing.T) {
	big := "export const blob = '" + strings.Repeat("x", maxFunctionCodeBytes+10) + "'\n"
	dir := writeTree(t, map[string]string{
		"f/big.ts":   big,
		"f/index.ts": "import { blob } from './big.ts'\nexport default async () => new Response(blob)\n",
	})
	_, err := bundleFunction(filepath.Join(dir, "f/index.ts"), bundleOptions{})
	if err == nil || !strings.Contains(err.Error(), "KB") {
		t.Errorf("error %v, want the size refusal", err)
	}
}

func TestCDNURL(t *testing.T) {
	for in, want := range map[string]string{
		"npm:stripe@14.21.0":    "https://esm.sh/stripe@14.21.0",
		"npm:@scope/pkg@1/sub":  "https://esm.sh/@scope/pkg@1/sub",
		"jsr:@std/encoding@1.0": "https://esm.sh/jsr/@std/encoding@1.0",
	} {
		if got := cdnURL(in); got != want {
			t.Errorf("cdnURL(%q) = %q, want %q", in, got, want)
		}
	}
}
