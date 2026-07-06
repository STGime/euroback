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
	// Rewrite Deno.serve(…) — walks balanced parens so the closing
	// `)` is stripped correctly regardless of body shape. Handles:
	//   Deno.serve((req) => body)               (arrow, any body)
	//   Deno.serve(async (req) => body)         (arrow-async)
	//   Deno.serve((req: Request) => body)      (typed arg)
	//   Deno.serve(handler)                     (named ident)
	//   Deno.serve(handler, { port: 8000 })     (with options — warn)
	// The old regex-only approach failed on: single-expression arrow
	// bodies (kept stray `)`), files with multiple Deno.serve calls
	// (only the last stripped), and second-arg options (dangling
	// `, {…})`). See #277 review H #1 / #2 / #3.
	out, serveRewrites, serveWarns := rewriteDenoServeCalls(out)
	for name, n := range serveRewrites {
		res.Rewrites[name] += n
	}
	res.Warnings = append(res.Warnings, serveWarns...)

	// If any `Deno.serve(` survives the rewrite pass, the tenant's
	// source used a shape we couldn't safely rewrite (non-`req` arg
	// name, unusual whitespace, comment injected inside the call).
	// Warn loudly so the tenant knows why the file will fail to
	// deploy. (#277 review M #6.)
	if strings.Contains(out, "Deno.serve(") {
		res.Warnings = append(res.Warnings, FunctionWarning{
			line: 0,
			note: "`Deno.serve(...)` survived the rewrite pass — the handler arg wasn't in a recognised shape (arrow with `req`, or bare identifier). Rewrite by hand to `module.exports = async (req, ctx) => …`",
		})
	}

	// Detect Supabase SDK imports (also catches aliased forms like
	// `import { createClient as sb }`). Even when the URL literal
	// isn't a direct match for reSupabaseCreateClient, a
	// @supabase/supabase-js import is enough signal that the tenant
	// is running the SDK inside the handler — warn. (#277 review H #4.)
	if reSupabaseSDKImport.MatchString(input) && !res.Unsupported {
		res.Warnings = append(res.Warnings, FunctionWarning{
			line: 0,
			note: "imports @supabase/supabase-js — Eurobase functions don't have an in-handler SDK client. If you use it inside the handler, rewrite queries to `ctx.db.sql(...)`",
		})
	}

	res.Source = out
	return res
}

// rewriteDenoServeCalls walks the input looking for `Deno.serve(`,
// finds the balanced closing `)`, and rewrites the whole call in one
// step. Robust against multiple calls, nested braces in the body,
// and single-expression arrow bodies.
func rewriteDenoServeCalls(input string) (string, map[string]int, []FunctionWarning) {
	rewrites := map[string]int{}
	var warns []FunctionWarning
	var out strings.Builder
	out.Grow(len(input))
	i := 0
	for i < len(input) {
		// Find next `Deno.serve(` with word boundary at start.
		idx := strings.Index(input[i:], "Deno.serve")
		if idx < 0 {
			out.WriteString(input[i:])
			break
		}
		start := i + idx
		// Word-boundary check on the char before `D` (mustn't be an
		// ident char — avoids matching `MyDeno.serve`).
		if start > 0 {
			c := input[start-1]
			if isIdentByte(c) {
				out.WriteString(input[i : start+len("Deno.serve")])
				i = start + len("Deno.serve")
				continue
			}
		}
		// Skip whitespace after `Deno.serve` before the `(`.
		openParen := start + len("Deno.serve")
		for openParen < len(input) && (input[openParen] == ' ' || input[openParen] == '\t') {
			openParen++
		}
		if openParen >= len(input) || input[openParen] != '(' {
			// Not a call — bare identifier reference, leave alone.
			out.WriteString(input[i : start+1])
			i = start + 1
			continue
		}
		closeParen := matchParen(input, openParen)
		if closeParen < 0 {
			// Malformed — bail out, leave the rest untouched.
			out.WriteString(input[i:])
			break
		}
		inner := input[openParen+1 : closeParen]
		// Emit everything before Deno.serve unchanged.
		out.WriteString(input[i:start])
		// Analyse inner.
		replacement, rule, warn := rewriteDenoServeInner(inner)
		if rule != "" {
			rewrites[rule]++
		}
		if warn != "" {
			warns = append(warns, FunctionWarning{line: 0, note: warn})
		}
		if replacement != "" {
			out.WriteString(replacement)
		} else {
			// Couldn't rewrite — leave the call as-is (warning above
			// covers this in the caller too).
			out.WriteString(input[start : closeParen+1])
		}
		i = closeParen + 1
	}
	return out.String(), rewrites, warns
}

// rewriteDenoServeInner takes the text INSIDE the outermost parens of
// a `Deno.serve(…)` call and returns:
//   - the replacement text for the whole call (empty on "leave alone")
//   - the rewrite rule name to bump in the counter (empty if none)
//   - a warning note (empty if none)
func rewriteDenoServeInner(inner string) (string, string, string) {
	// Split on top-level comma to check for a second (options) arg.
	// If present, we still rewrite the first arg but warn that options
	// were dropped.
	firstArg, secondArg := splitTopLevelComma(inner)
	firstArg = strings.TrimSpace(firstArg)
	var warn string
	if strings.TrimSpace(secondArg) != "" {
		warn = "Deno.serve had a second (options) argument that's not portable to Eurobase — options dropped; port / signal / onListen configured on Eurobase's side"
	}

	// Arrow: `async (req[: Type]) => body` or `(req[: Type]) => body`
	if body, isAsync, ok := extractArrowBody(firstArg); ok {
		asyncKW := ""
		if isAsync {
			// Preserve async modifier — most handlers are async
			// anyway but a sync one shouldn't get force-async'd.
			asyncKW = "async "
		} else {
			// Eurobase's runner expects an async handler; if the
			// source was sync, force async so we don't break
			// promise-consuming code paths.
			asyncKW = "async "
		}
		rule := "rewrite:Deno.serve((req) => …) → module.exports"
		return "module.exports = " + asyncKW + "(req, ctx) => " + body, rule, warn
	}

	// Named-identifier form: `handler`
	if isPlainIdent(firstArg) {
		rule := "rewrite:Deno.serve(handler) → module.exports = handler"
		return "module.exports = " + firstArg, rule, warn
	}

	// Neither shape — leave the whole call alone. Caller emits an
	// additional warning about the surviving `Deno.serve(...)`.
	return "", "", warn
}

// extractArrowBody inspects `expr` for the shape `async? (req[: Type]) => <body>`.
// Returns the body text, whether async was present, and true on match.
// Whitespace-tolerant.
func extractArrowBody(expr string) (string, bool, bool) {
	rest := strings.TrimSpace(expr)
	isAsync := false
	if strings.HasPrefix(rest, "async") && len(rest) > 5 && !isIdentByte(rest[5]) {
		isAsync = true
		rest = strings.TrimSpace(rest[5:])
	}
	// Must open with `(`
	if len(rest) == 0 || rest[0] != '(' {
		return "", false, false
	}
	// Match balanced parens for the arg list.
	depth := 0
	argEnd := -1
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				argEnd = i
			}
		}
		if argEnd >= 0 {
			break
		}
	}
	if argEnd < 0 {
		return "", false, false
	}
	// Arg text between the outer `(` and `)`. Must be `req` (with
	// optional type annotation) so we don't accidentally rewrite a
	// two-arg or destructured handler.
	arg := strings.TrimSpace(rest[1:argEnd])
	if !argIsReq(arg) {
		return "", false, false
	}
	// Skip past `)` then whitespace, then must see `=>`.
	i := argEnd + 1
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	if i+1 >= len(rest) || rest[i] != '=' || rest[i+1] != '>' {
		return "", false, false
	}
	body := strings.TrimSpace(rest[i+2:])
	return body, isAsync, true
}

// argIsReq accepts `req` or `req: Request` (or any `req: <type>`),
// but not `request`, `event`, `(req, res)` etc.
func argIsReq(arg string) bool {
	if arg == "req" {
		return true
	}
	if strings.HasPrefix(arg, "req") && len(arg) > 3 {
		c := arg[3]
		if c == ':' || c == ' ' || c == '\t' {
			return true
		}
	}
	return false
}

// isPlainIdent returns true if `s` is a bare JS identifier (no
// operators, no parens). Used to detect the named-handler form of
// Deno.serve.
func isPlainIdent(s string) bool {
	if s == "" {
		return false
	}
	if !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

func isIdentStart(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

func isIdentByte(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

// splitTopLevelComma splits `s` at the first comma NOT nested inside
// parens, brackets, braces, or strings. Returns (before, after);
// after is "" when no such comma exists.
func splitTopLevelComma(s string) (string, string) {
	depth := 0
	inString := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString != 0 {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == inString {
				inString = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inString = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				return s[:i], s[i+1:]
			}
		}
	}
	return s, ""
}

// matchParen given the index of a `(` returns the index of its
// matching `)`, respecting string literals and nested parens. Returns
// -1 on unbalanced input.
func matchParen(s string, openIdx int) int {
	depth := 0
	inString := byte(0)
	for i := openIdx; i < len(s); i++ {
		c := s[i]
		if inString != 0 {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == inString {
				inString = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inString = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
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

	// `createClient(SUPABASE_URL, …)` or `createClient(supabaseUrl, …)`
	// — the presence indicator that the function uses Supabase's SDK
	// client with a hardcoded URL literal. Aliased-import cases
	// (`import { createClient as sb }`) are caught separately via
	// reSupabaseSDKImport.
	reSupabaseCreateClient = regexp.MustCompile(
		`\bcreateClient\s*\(\s*(?:SUPABASE_URL|process\.env\.SUPABASE_URL|Deno\.env\.get\(\s*['"]SUPABASE_URL['"]\s*\)|supabaseUrl)`,
	)

	// `import … from '…@supabase/supabase-js…'` — signal that the
	// tenant likely uses the SDK inside the handler, even if the
	// createClient invocation was renamed via `as` or reads its URL
	// from a variable the createClient regex doesn't cover. We warn
	// rather than mark unsupported — the tenant might legitimately
	// import types-only. (#277 review H #4.)
	reSupabaseSDKImport = regexp.MustCompile(`(?m)^\s*import\b.*@supabase/supabase-js`)
)
