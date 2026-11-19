package cli

import (
	"encoding/json"
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

// A helper in an imported file that happens to be called "handler" (or
// "exports") must not meet the names the runner looks for: the bundle
// runs inside a function of its own.
func TestBundleFunction_BundleIsWrapped(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"f/lib.ts":   "export function handler(x: unknown): string { return 'LIBRARY ' + typeof x }\nexport const exports = 1\n",
		"f/index.ts": "import { handler as lib, exports as e } from './lib.ts'\nexport default async () => new Response(lib(1) + e)\n",
	})
	res, err := bundleFunction(filepath.Join(dir, "f/index.ts"), bundleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := res.Code[strings.Index(res.Code, "(() => {"):]
	if !strings.HasPrefix(body, "(() => {") || !strings.HasSuffix(strings.TrimSpace(body), "})();") {
		t.Errorf("bundle isn't wrapped in a function:\n%s", res.Code)
	}
	if _, err := functions.Transpile(res.Code); err != nil {
		t.Fatalf("the server would refuse the bundle: %v", err)
	}
}

func TestBundleFunction_NeedsAnExport(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"f/util.ts":  "export const one = 1\n",
		"f/index.ts": "import { one } from './util.ts'\nasync function handler() { return new Response(String(one)) }\n",
	})
	_, err := bundleFunction(filepath.Join(dir, "f/index.ts"), bundleOptions{})
	if err == nil || !strings.Contains(err.Error(), "export default") {
		t.Errorf("error %v, want the missing-export message", err)
	}
}

// A module that redirects is known by where it ended up: its relative
// imports resolve there, and it is bundled once.
func TestBundleFunction_Redirects(t *testing.T) {
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/pkg":
			http.Redirect(w, r, "/pkg@1/dist/mod.js", http.StatusFound)
		case "/pkg@1/dist/mod.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte("import { h } from './helper.js'\nexport const v = 'mod+' + h\n"))
		case "/pkg@1/dist/helper.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte("export const h = 'helper'\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := writeTree(t, map[string]string{
		"f/index.ts": "import { v } from '" + srv.URL + "/pkg'\nimport { v as again } from '" + srv.URL + "/pkg@1/dist/mod.js'\n" +
			"export default async () => new Response(v + again)\n",
	})
	res, err := bundleFunction(filepath.Join(dir, "f/index.ts"), bundleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Remote) != 2 {
		t.Errorf("remote modules %v, want mod.js and helper.js once each", res.Remote)
	}
	if strings.Count(res.Code, `"mod+"`) != 1 {
		t.Errorf("the redirected module is in the bundle %d times", strings.Count(res.Code, `"mod+"`))
	}
}

// A fetched module must not be able to pull something from the
// developer's machine or network into the bundle. The developer's own
// URL may be local; what that module imports from another local address
// may not.
func TestBundleFunction_RemoteModuleCannotReachPrivateHosts(t *testing.T) {
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"internal":"secret-config"}`))
	}))
	defer secret.Close()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		switch r.URL.Path {
		case "/direct.js":
			_, _ = w.Write([]byte("import cfg from '" + secret.URL + "/admin/config'\nexport const leaked = JSON.stringify(cfg)\n"))
		case "/via-redirect.js":
			_, _ = w.Write([]byte("import cfg from './hop'\nexport const leaked = JSON.stringify(cfg)\n"))
		case "/hop":
			http.Redirect(w, r, secret.URL+"/admin/config", http.StatusFound)
		}
	}))
	defer srv.Close()
	for _, mod := range []string{"/direct.js", "/via-redirect.js"} {
		dir := writeTree(t, map[string]string{
			"f/index.ts": "import { leaked } from '" + srv.URL + mod + "'\nexport default async () => new Response(leaked)\n",
		})
		res, err := bundleFunction(filepath.Join(dir, "f/index.ts"), bundleOptions{})
		if err == nil {
			t.Errorf("%s: bundled a private address's answer:\n%s", mod, res.Code)
		} else if strings.Contains(err.Error(), "secret-config") {
			t.Errorf("%s: the error carries the private answer: %v", mod, err)
		}
	}
}

func TestBundleFunction_ImportMapProblemsOnlyMatterWhenNeeded(t *testing.T) {
	// A broken config above the function: a function that needs no
	// import map still deploys.
	plain := "export default async () => new Response('ok')\n"
	dir := writeTree(t, map[string]string{
		"deno.json":            "{ this is not json",
		"functions/a/index.ts": plain,
		"functions/b/index.ts": "import x from 'mapped'\nexport default async () => new Response(String(x))\n",
	})
	res, err := bundleFunction(filepath.Join(dir, "functions/a/index.ts"), bundleOptions{})
	if err != nil || res.Code != plain {
		t.Errorf("a function without imports must deploy next to a broken deno.json: %v", err)
	}
	// One that needs it is told which file is broken.
	_, err = bundleFunction(filepath.Join(dir, "functions/b/index.ts"), bundleOptions{})
	if err == nil || !strings.Contains(err.Error(), "deno.json") {
		t.Errorf("error %v, want it to name the unreadable deno.json", err)
	}
}

func TestImportMapLookup(t *testing.T) {
	m := &importMap{dir: "/p", imports: map[string]string{
		"@std/encoding": "jsr:@std/encoding@^1.0.0",
		"zod":           "npm:zod@3",
		"@shared/":      "./_shared/",
		"exact":         "https://example.test/x.ts",
	}}
	for spec, want := range map[string]string{
		"@std/encoding":        "jsr:@std/encoding@^1.0.0",
		"@std/encoding/base64": "jsr:@std/encoding@^1.0.0/base64",
		"zod/v4":               "npm:zod@3/v4",
		"@shared/cors.ts":      "./_shared/cors.ts",
		"exact":                "https://example.test/x.ts",
	} {
		if got, _, ok := m.lookup(spec); !ok || got != want {
			t.Errorf("lookup(%q) = %q %v, want %q", spec, got, ok, want)
		}
	}
	for _, spec := range []string{"exact/sub", "zodiac", "unknown"} {
		if got, _, ok := m.lookup(spec); ok {
			t.Errorf("lookup(%q) = %q, want no match", spec, got)
		}
	}
}

func TestRelaxJSON(t *testing.T) {
	in := "{\n  // comment\n  \"imports\": {\n    \"a\": \"https://x.test/a//b\", /* c */\n    \"b\": \"y,}\",\n  },\n}\n"
	var doc struct{ Imports map[string]string }
	if err := json.Unmarshal(relaxJSON([]byte(in)), &doc); err != nil {
		t.Fatalf("relaxed JSON doesn't parse: %v\n%s", err, relaxJSON([]byte(in)))
	}
	if doc.Imports["a"] != "https://x.test/a//b" || doc.Imports["b"] != "y,}" {
		t.Errorf("strings were changed: %+v", doc.Imports)
	}
}

// What deployed before bundling existed still deploys: the server reads
// every file as TypeScript, and so does the bundler.
func TestBundleFunction_JSFilesAreReadLikeTheServerReadsThem(t *testing.T) {
	for name, src := range map[string]string{
		"type annotations in .js": "import { one } from './util.js'\nexport default async (req: Request) => new Response(String(one))\n",
		"unused type import":      "import { EdgeHandler } from '@eurobase/sdk/functions'\nimport { one } from './util.js'\nexport default async () => new Response(String(one))\n",
		"optional require":        "import { one } from './util.js'\nlet opt = null\ntry { opt = require('not-installed') } catch (e) {}\nexport default async () => new Response(String(one) + opt)\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeTree(t, map[string]string{"f/util.js": "export const one = 1\n", "f/index.js": src})
			res, err := bundleFunction(filepath.Join(dir, "f/index.js"), bundleOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := functions.Transpile(res.Code); err != nil {
				t.Errorf("the server would refuse it: %v", err)
			}
		})
	}
}

func TestCheckReadsTheSameAsTypeScript(t *testing.T) {
	if err := checkReadsTheSameAsTypeScript("var a = 1, b = 2, c = 3;\nmodule.exports = a < b ? c : a;\n"); err != nil {
		t.Errorf("ordinary code refused: %v", err)
	}
	// `a < b > (c, d)` is two comparisons in JavaScript and a generic
	// call in TypeScript.
	if err := checkReadsTheSameAsTypeScript("var a = 1, b = 2, c = 3, d = 4;\nmodule.exports = a < b > (c, d);\n"); err == nil {
		t.Error("code that reads differently as TypeScript was accepted")
	}
}

func TestBundleHeader_NoAbsolutePath(t *testing.T) {
	h := bundleHeader("/Users/someone/projects/app/functions/pay/index.ts", 3)
	if strings.Contains(h, "someone") || !strings.Contains(h, "pay/index.ts") {
		t.Errorf("header: %s", h)
	}
}
