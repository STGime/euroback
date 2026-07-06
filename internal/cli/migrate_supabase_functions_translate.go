package cli

// #272 (part of #267): Supabase → Eurobase edge-function translator.
//
// Given a Supabase edge function's TypeScript source, produces
// TypeScript that runs against Eurobase's functions runner contract
// (see functions-runner/worker_bootstrap.js: exports must be assigned
// via `module.exports = ...` or `export default ...`, and the second
// argument is `ctx` with `ctx.env` / `ctx.db` accessors).
//
// This file is PURE text transformation — no filesystem, no shelling
// out — so the rewrite rules can be pinned by unit tests without
// spinning up a Deno or Node runtime. Same pattern as the DDL
// translator in #269.
//
// The rewrite space is small on purpose:
//   1. `Deno.serve((req) => …)`      → `module.exports = async (req, ctx) => …`
//   2. `Deno.env.get('KEY')`         → `ctx.env.KEY` (unless KEY is a
//      Supabase-only var that has a Eurobase-named replacement).
//   3. Detect `createClient(SUPABASE_URL, …)` inside the handler and
//      mark the function as `unsupported` — the tenant must rewrite
//      to use `ctx.db.sql(…)` because Eurobase has no in-function
//      SDK client (data plane goes through ctx).
//   4. `import … from 'https://…'` — warn only (Eurobase's runner
//      supports remote imports via esbuild-on-deploy, but the tenant
//      should eyeball third-party deps).

import (
	"regexp"
	"strings"
)

// FunctionWarning is one thing the translator noticed that the tenant
// should review before deploying.
type FunctionWarning struct {
	line int
	note string
}

// FunctionTranslationResult is what TranslateFunction returns.
type FunctionTranslationResult struct {
	// Source is the translated TypeScript. Empty when Unsupported is
	// true — the tenant must rewrite by hand, and we don't want the
	// CLI to write a broken file to disk.
	Source string
	// Rewrites counts how many of each rule fired, so the CLI can
	// print a per-function "3 rewrites, 1 warning" summary.
	Rewrites map[string]int
	// Warnings is the list of things the tenant should eyeball
	// (https:// imports, unusual patterns, …).
	Warnings []FunctionWarning
	// Unsupported is true when the source uses something Eurobase's
	// runner can't emulate today (in-handler Supabase SDK client).
	// UnsupportedReason carries a human-readable "why + how to fix".
	Unsupported       bool
	UnsupportedReason string
}

// TranslateFunction rewrites one Supabase edge function's TypeScript
// source for Eurobase's runner contract. See the file-level doc for
// the rewrite rules.
func TranslateFunction(input string) FunctionTranslationResult {
	res := FunctionTranslationResult{Rewrites: map[string]int{}}

	// Unsupported detection FIRST — if the source uses the in-handler
	// Supabase SDK client, no amount of rewriting produces working
	// code. Report and stop so the tenant sees the exact snippet to
	// replace.
	if reSupabaseCreateClient.MatchString(input) {
		res.Unsupported = true
		res.UnsupportedReason = "uses `createClient(SUPABASE_URL, …)` inside the handler — " +
			"Eurobase functions don't expose an in-handler SDK client. " +
			"Rewrite queries to use `ctx.db.sql('SELECT …')` and storage/auth via `ctx` accessors."
		return res
	}

	// Walk line by line — TypeScript sources don't need a full
	// tokenizer for these rewrites (unlike SQL, where a policy body
	// spans multiple lines with strings inside). Warnings track the
	// 1-indexed line number so the tenant can jump to the source.
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		lineNum := i + 1
		// `import … from 'https://…'` — warn only.
		if reHTTPSImport.MatchString(line) {
			res.Warnings = append(res.Warnings, FunctionWarning{
				line: lineNum,
				note: "imports from an https:// URL — supported by Eurobase's runner via esbuild-on-deploy, but confirm the dep is fetchable and pinned",
			})
		}
		// `Deno.env.get('SUPABASE_URL')` etc. — warn AND rewrite.
		// Eurobase's runner exposes `ctx.env.EUROBASE_URL` but the
		// tenant may want to reconsider whether they need it at all.
		if m := reSupabaseEnv.FindStringSubmatch(line); m != nil {
			res.Warnings = append(res.Warnings, FunctionWarning{
				line: lineNum,
				note: "reads Supabase-specific env var `" + m[1] + "` — the corresponding Eurobase value has a different name / semantics; verify before deploying",
			})
		}
	}

	out := input

	// Rewrite: Deno.env.get('KEY') → ctx.env.KEY. Works for both
	// single and double quotes. Restrict the KEY pattern to a valid
	// JS identifier (`[A-Za-z_][A-Za-z0-9_]*`) so we don't
	// accidentally rewrite `Deno.env.get(runtimeKey)` — that's a
	// dynamic access, which needs manual handling.
	if n := countAndReplaceStr(&out, reDenoEnvGet, "ctx.env.$1"); n > 0 {
		res.Rewrites["rewrite:Deno.env.get('KEY') → ctx.env.KEY"] += n
	}
	// Rewrite: Deno.env.get(<any>) with non-identifier args — warn
	// so the tenant knows the dynamic form needs manual handling.
	if reDenoEnvGetDynamic.MatchString(out) {
		res.Warnings = append(res.Warnings, FunctionWarning{
			line: 0,
			note: "uses `Deno.env.get(<expr>)` with a non-literal argument — Eurobase's `ctx.env` is a plain object; rewrite the dynamic access to `ctx.env[expr]`",
		})
	}
	// Rewrite: Deno.serve((req) => …) → module.exports = async (req, ctx) => …
	// The arrow form covers all common shapes:
	//   Deno.serve((req) => …)
	//   Deno.serve(async (req) => …)
	//   Deno.serve(handler)                     (named function reference)
	// Named-function-reference form gets a specific rewrite so the
	// symbol is exported.
	if n := countAndReplaceStr(&out, reDenoServeArrowAsync,
		"module.exports = async (req, ctx) => "); n > 0 {
		res.Rewrites["rewrite:Deno.serve(async (req) => …) → module.exports"] += n
	}
	if n := countAndReplaceStr(&out, reDenoServeArrow,
		"module.exports = async (req, ctx) => "); n > 0 {
		res.Rewrites["rewrite:Deno.serve((req) => …) → module.exports"] += n
	}
	if n := countAndReplaceStr(&out, reDenoServeNamed,
		"module.exports = $1"); n > 0 {
		res.Rewrites["rewrite:Deno.serve(handler) → module.exports = handler"] += n
	}
	// Closing `)` of the original Deno.serve(…) call — leaves a
	// stray `)` at the end of the arrow body. The rewrite above
	// captured up to `=> `; the tenant's body follows, and we need
	// to strip the trailing `)` that used to close the Deno.serve
	// call. Handle in a second pass on the whole file: find the
	// pattern `module.exports = async (req, ctx) => <body>);` and
	// drop the final `)`. Cheap approach: reStripDenoServeClose runs
	// against every logical statement close.
	if n := countAndReplaceStr(&out, reStripTrailingParenAfterMEx, "$1"); n > 0 {
		res.Rewrites["cleanup:strip trailing `)` from Deno.serve closer"] += n
	}

	res.Source = out
	return res
}

// countAndReplaceStr is a small sibling of countAndReplace (in
// migrate_supabase_translator.go) that also supports capture groups
// in the replacement — the DDL translator's rules were simple
// string-for-string, function translator's rules need `$1`.
func countAndReplaceStr(s *string, re *regexp.Regexp, repl string) int {
	matches := re.FindAllStringIndex(*s, -1)
	if len(matches) == 0 {
		return 0
	}
	*s = re.ReplaceAllString(*s, repl)
	return len(matches)
}

// ── regex table ──────────────────────────────────────────────────────

var (
	// `Deno.env.get('IDENT')` / `.get("IDENT")` — the identifier form
	// only. Dynamic argument forms (`Deno.env.get(runtimeKey)`) are
	// picked up separately for warning.
	reDenoEnvGet = regexp.MustCompile(`\bDeno\.env\.get\(\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]\s*\)`)
	// `Deno.env.get(<anything else>)` — captures dynamic access
	// AFTER the identifier-form rewrite has run (so residual matches
	// are the dynamic ones). Not anchored to the whole line so an
	// inline expression matches.
	reDenoEnvGetDynamic = regexp.MustCompile(`\bDeno\.env\.get\(\s*[^'"\s)][^)]*\)`)

	// `Deno.env.get('SUPABASE_URL' | 'SUPABASE_ANON_KEY' | 'SUPABASE_SERVICE_ROLE_KEY')`
	// — for the warn-only side of the rewrite. We still rewrite the
	// syntax (to `ctx.env.SUPABASE_URL` etc.) so the file compiles,
	// but the tenant needs to know they're reading a var that doesn't
	// exist on Eurobase's side by default.
	reSupabaseEnv = regexp.MustCompile(
		`\bDeno\.env\.get\(\s*['"](SUPABASE_URL|SUPABASE_ANON_KEY|SUPABASE_SERVICE_ROLE_KEY|SUPABASE_DB_URL|SUPABASE_JWT_SECRET)['"]\s*\)`,
	)

	// `import { … } from 'https://…'` / `import … from "https://…"` /
	// `import 'https://…'`. Line-anchored to reduce false positives.
	reHTTPSImport = regexp.MustCompile(`^\s*import\b[^'"]*['"]https://`)

	// Deno.serve variants. Two capture forms + a NAMED form so the
	// symbol handler gets re-exported.
	//
	// Arrow-async: `Deno.serve(async (req) => `
	reDenoServeArrowAsync = regexp.MustCompile(`\bDeno\.serve\s*\(\s*async\s*\(\s*req\s*(?::[^,)]+)?\s*\)\s*=>\s*`)
	// Plain arrow: `Deno.serve((req) => ` (also picks up `(req: Request) =>`)
	reDenoServeArrow = regexp.MustCompile(`\bDeno\.serve\s*\(\s*\(\s*req\s*(?::[^,)]+)?\s*\)\s*=>\s*`)
	// Named reference: `Deno.serve(handlerName)` (handlerName is a
	// plain JS ident). We rewrite to `module.exports = handlerName`
	// and drop the trailing `)` via reStripTrailingParenAfterMEx.
	reDenoServeNamed = regexp.MustCompile(`\bDeno\.serve\s*\(\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\)`)

	// After the arrow rewrite, the tenant's function body is followed
	// by the original `)` that closed `Deno.serve(…)`. Strip a
	// trailing `)` that immediately precedes the file's final
	// newline / semicolon so the emitted code parses cleanly.
	//
	// Cheap heuristic: match `<lines>});` (or `})`) at the END of the
	// input where the preceding text starts with `module.exports = `.
	// Fully general is impossible without a JS parser — we accept a
	// small false-negative rate and let the tenant eyeball the diff.
	reStripTrailingParenAfterMEx = regexp.MustCompile(
		`(?s)(module\.exports\s*=\s*async\s*\(req,\s*ctx\)\s*=>\s*\{[^}]*(?:\}[^{}]*)*\})\s*\)\s*;?\s*$`,
	)

	// `createClient(SUPABASE_URL, …)` or `createClient(supabaseUrl, …)`
	// — the presence indicator that the function uses Supabase's SDK
	// client, which Eurobase doesn't emulate.
	reSupabaseCreateClient = regexp.MustCompile(
		`\bcreateClient\s*\(\s*(?:SUPABASE_URL|process\.env\.SUPABASE_URL|Deno\.env\.get\(\s*['"]SUPABASE_URL['"]\s*\)|supabaseUrl)`,
	)
)
