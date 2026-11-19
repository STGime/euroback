package cli

// Supabase compatibility for `eurobase edge-functions deploy`.
//
// A function written for Supabase starts an HTTP server with
// Deno.serve(handler), reads its settings with Deno.env.get, takes the
// caller from the Authorization header and talks to the project through
// createClient(url, key). None of that exists in a Eurobase function,
// which exports a handler and gets everything on ctx.
//
// Instead of rewriting the developer's code, the deploy builds it inside
// a wrapper that supplies those things:
//
//   - @supabase/supabase-js is replaced by compat/supabase.ts, which runs
//     the same calls as SQL through ctx.db
//   - Deno.serve(handler) / serve(handler) hand the handler to the wrapper
//   - Deno.env.get(NAME) reads the function's env vars, then the Vault
//   - a call from a signed-in user gets an Authorization header again
//     (the platform has verified the real one and keeps it)
//
// The function's code runs when a request arrives, not when the function
// is loaded: `new Stripe(Deno.env.get("STRIPE_KEY"))` at the top of a
// file needs the settings, and those arrive with the request.

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	compatSupabase = "supabase"
	compatNone     = "none"

	compatClientModule = "eurobase:supabase"
	compatServeModule  = "eurobase:serve"
)

//go:embed compat/supabase.ts
var supabaseCompatSource string

// compatServeSource stands in for deno.land/std/http/server.ts.
const compatServeSource = `export function serve(handler: unknown, options?: unknown): unknown {
  return (globalThis as any).__eurobase.serve(handler, options);
}
export const serveTls = serve;
export class Server {}
`

func supabaseCompatModules() map[string]string {
	return map[string]string{
		compatClientModule: supabaseCompatSource,
		compatServeModule:  compatServeSource,
	}
}

var (
	supabaseJSRe = regexp.MustCompile(`(^|[/:])@supabase/supabase-js(@[^/?]*)?([/?].*)?$`)
	stdServeRe   = regexp.MustCompile(`^https://deno\.land/std(@[^/]+)?/http/server\.ts$`)
)

// supabaseCompatAlias names the module that stands in for spec, or "".
func supabaseCompatAlias(spec string) string {
	switch {
	case supabaseJSRe.MatchString(spec):
		return compatClientModule
	case stdServeRe.MatchString(spec):
		return compatServeModule
	}
	return ""
}

var (
	supabaseImportRe = regexp.MustCompile(`["'][^"']*@supabase/supabase-js[^"']*["']`)
	stdServeImportRe = regexp.MustCompile(`["']https://deno\.land/std(@[^/"']+)?/http/server\.ts["']`)
	denoServeRe      = regexp.MustCompile(`\bDeno\s*\.\s*serve\s*\(`)
)

// looksLikeSupabaseFunction reports whether a function's entry file is
// written the Supabase way: it starts its own server or imports the
// Supabase client.
func looksLikeSupabaseFunction(source string) bool {
	return denoServeRe.MatchString(source) || stdServeImportRe.MatchString(source) || supabaseImportRe.MatchString(source)
}

var envReadRe = regexp.MustCompile(`(?:Deno\s*\.\s*env\s*\.\s*get\s*\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\)|process\s*\.\s*env\s*\.\s*([A-Za-z_][A-Za-z0-9_]*)|process\s*\.\s*env\s*\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\])`)

// envNamesRead lists the settings the entry file and its local imports
// read, without Supabase's own, and whether the service role key is one
// of them.
func envNamesRead(entrySource, dir string, local []string) (names []string, serviceRole bool) {
	seen := map[string]bool{}
	scan := func(src string) {
		for _, m := range envReadRe.FindAllStringSubmatch(src, -1) {
			for _, name := range m[1:] {
				if name != "" {
					seen[name] = true
				}
			}
		}
	}
	scan(entrySource)
	for _, rel := range local {
		if strings.Contains(rel, ":") {
			continue // one of the CLI's own modules
		}
		if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); err == nil {
			scan(string(b))
		}
	}
	serviceRole = seen["SUPABASE_SERVICE_ROLE_KEY"]
	for name := range seen {
		if !strings.HasPrefix(name, "SUPABASE_") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, serviceRole
}

// supabaseCompatWrapper returns the code around the bundle. Together they
// are the function's handler: on a request it collects the settings, runs
// the developer's code and calls the handler that code registered.
func supabaseCompatWrapper(envNames []string) (banner, footer string) {
	names, _ := json.Marshal(envNames)
	if envNames == nil {
		names = []byte("[]")
	}
	banner = `globalThis.handler = async (__req, __ctx) => {
// Settings: the function's env vars first, then the project's Vault for
// the names the code reads.
const __env = Object.assign({}, __ctx.env);
await Promise.all(` + string(names) + `.filter((n) => !(n in __env)).map(async (n) => {
  try {
    const v = await __ctx.vault.get(n);
    if (v !== null && v !== undefined) __env[n] = v;
  } catch (_e) { /* not set */ }
}));
// Supabase's own settings. The client module only looks at which key it
// was given, so the service role key is a new random value for every
// request: code that compares it with something a caller sent can never
// be satisfied by a guess. A project that needs a real shared secret
// there (a cron job calling the function) sets SUPABASE_SERVICE_ROLE_KEY
// itself, as a function env var or in the Vault.
if (!(typeof __env.SUPABASE_SERVICE_ROLE_KEY === "string" && __env.SUPABASE_SERVICE_ROLE_KEY.length >= 16)) {
  __env.SUPABASE_SERVICE_ROLE_KEY = "eb-service-" + crypto.randomUUID() + crypto.randomUUID();
}
if (!("SUPABASE_URL" in __env)) __env.SUPABASE_URL = "https://eurobase.invalid";
if (!("SUPABASE_ANON_KEY" in __env)) __env.SUPABASE_ANON_KEY = "eurobase-anon";
let __served = null;
const __serve = (a, b) => {
  __served = typeof a === "function" ? a : typeof b === "function" ? b : a && a.handler;
  return { finished: new Promise(() => {}), shutdown() {}, ref() {}, unref() {}, addr: { hostname: "localhost", port: 0 } };
};
const __envApi = { get: (n) => __env[n], has: (n) => n in __env, toObject: () => Object.assign({}, __env), set() {}, delete() {} };
const Deno = new Proxy(globalThis.Deno || {}, { get: (t, k) => (k === "env" ? __envApi : k === "serve" ? __serve : t[k]) });
const process = new Proxy(globalThis.process || {}, { get: (t, k) => (k === "env" ? __env : t[k]) });
globalThis.__eurobase = { ctx: __ctx, serve: __serve, serviceKey: __env.SUPABASE_SERVICE_ROLE_KEY };
const module = { exports: {} };
const exports = module.exports;
(() => {`
	footer = `})();
const __h = __served || (typeof module.exports === "function" ? module.exports : module.exports && module.exports.default);
if (typeof __h !== "function") throw new Error("this function never calls Deno.serve(handler) and has no default export");
// The platform verified the caller and keeps their token; code that
// checks for the header finds one.
let __r = __req;
if (__ctx.user && !__req.headers.get("authorization")) {
  const __hd = new Headers(__req.headers);
  __hd.set("authorization", "Bearer eurobase-verified-user");
  __r = new Request(__req, { headers: __hd });
}
return __h(__r, __ctx);
};`
	return banner, footer
}

// BundleSupabaseFunction builds the function at entryPath the way
// `eurobase edge-functions deploy` does for a function written for
// Supabase, and returns the code to deploy. For tests outside this
// package that run such a function for real.
func BundleSupabaseFunction(entryPath string) (string, error) {
	res, err := bundleFunction(entryPath, bundleOptions{Compat: compatSupabase})
	if err != nil {
		return "", err
	}
	return res.Code, nil
}
