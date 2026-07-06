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
	out, serveRewrites, serveWarns := rewriteServeCalls(out, "Deno.serve")
	for name, n := range serveRewrites {
		res.Rewrites[name] += n
	}
	res.Warnings = append(res.Warnings, serveWarns...)

	// #277 round-2 ship-blocker #1: the canonical Supabase template
	// still generated by `supabase functions new` uses bare
	// `serve(handler)` imported from `deno.land/std/http/server.ts`.
	// Rewrite these too (guarded on the import so we don't trample
	// a tenant helper named `serve`).
	if reDenoStdServeImport.MatchString(input) {
		out2, extra, extraWarns := rewriteServeCalls(out, "serve")
		out = out2
		for name, n := range extra {
			res.Rewrites[name] += n
		}
		res.Warnings = append(res.Warnings, extraWarns...)
		// If we rewrote anything, drop the now-orphan
		// `import { serve } from '.../http/server.ts'` line by
		// commenting it out — the tenant should remove it, but a
		// comment makes the diff explicit.
		if len(extra) > 0 {
			out = reDenoStdServeImport.ReplaceAllString(out, "// $0  // removed: Eurobase runner provides the handler contract natively")
			res.Rewrites["cleanup:comment out deno.land/std/http/server serve import"] += 1
		}
	}

	// If any `Deno.serve(` / `serve(` at statement head survives,
	// the tenant's source used a shape we couldn't safely rewrite
	// (non-`req` arg name, unusual whitespace). Warn loudly so the
	// tenant knows why the file will fail to deploy. (#277 review M #6.)
	if strings.Contains(out, "Deno.serve(") {
		res.Warnings = append(res.Warnings, FunctionWarning{
			line: 0,
			note: "`Deno.serve(...)` survived the rewrite pass — the handler arg wasn't in a recognised shape (arrow with `req`, or bare identifier). Rewrite by hand to `module.exports = async (req, ctx) => …`",
		})
	}

	// Detect Supabase SDK imports (also catches aliased forms like
	// `import { createClient as sb }`). Skip type-only imports —
	// those don't run any SDK code at runtime, so warning would be
	// a false positive. (#277 review H #4 + M #7.)
	if reSupabaseSDKImport.MatchString(input) &&
		!reSupabaseSDKTypeImport.MatchString(input) &&
		!res.Unsupported {
		res.Warnings = append(res.Warnings, FunctionWarning{
			line: 0,
			note: "imports @supabase/supabase-js — Eurobase functions don't have an in-handler SDK client. If you use it inside the handler, rewrite queries to `ctx.db.sql(...)`",
		})
	}

	res.Source = out
	return res
}

// rewriteServeCalls walks the input looking for `<callName>(`,
// finds the balanced closing `)`, and rewrites the whole call in one
// step. Robust against multiple calls, nested braces in the body,
// single-expression arrow bodies, and non-code parens (strings,
// comments, regex literals).
//
// callName is the exact function-name string to match — usually
// "Deno.serve", but also called with "serve" when the source imports
// `serve` from `deno.land/std/http/server.ts`.
func rewriteServeCalls(input, callName string) (string, map[string]int, []FunctionWarning) {
	rewrites := map[string]int{}
	var warns []FunctionWarning
	var out strings.Builder
	out.Grow(len(input))
	i := 0
	for i < len(input) {
		idx := strings.Index(input[i:], callName)
		if idx < 0 {
			out.WriteString(input[i:])
			break
		}
		start := i + idx
		// Word-boundary check on the char before the call name.
		// Skips `Deno.serve` matching inside `MyDeno.serve`, and
		// `serve` matching inside `preserve` / `observe`.
		if start > 0 {
			c := input[start-1]
			if isIdentByte(c) || c == '.' {
				// `.` before catches e.g. `foo.serve(...)`, which
				// isn't OUR serve.
				if !(callName == "Deno.serve" && c == '.' && start >= 5 && input[start-5:start] == "Deno.") {
					out.WriteString(input[i : start+len(callName)])
					i = start + len(callName)
					continue
				}
			}
		}
		// Skip whitespace after the call name before the `(`.
		openParen := start + len(callName)
		for openParen < len(input) && (input[openParen] == ' ' || input[openParen] == '\t') {
			openParen++
		}
		if openParen >= len(input) || input[openParen] != '(' {
			out.WriteString(input[i : start+1])
			i = start + 1
			continue
		}
		closeParen := matchParen(input, openParen)
		if closeParen < 0 {
			out.WriteString(input[i:])
			break
		}
		inner := input[openParen+1 : closeParen]
		out.WriteString(input[i:start])
		replacement, rule, warn := rewriteDenoServeInner(inner)
		if rule != "" {
			// Name the rule with the source keyword so the CLI's
			// per-function counter distinguishes Deno.serve from
			// bare serve.
			rewrites[strings.ReplaceAll(rule, "Deno.serve", callName)]++
		}
		if warn != "" {
			warns = append(warns, FunctionWarning{line: 0, note: warn})
		}
		if replacement != "" {
			out.WriteString(replacement)
		} else {
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
// parens/brackets/braces, string literals, comments, or regex
// literals. Returns (before, after); after is "" when no such comma
// exists. Uses the same state machine as matchParen.
func splitTopLevelComma(s string) (string, string) {
	depth := 0
	var st jsScanState
	for i := 0; i < len(s); i++ {
		codeByte, advance := st.step(s, i)
		if advance > 0 {
			i += advance
			continue
		}
		if !codeByte {
			continue
		}
		switch s[i] {
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

// ── JS scanner state machine ─────────────────────────────────────────

// jsScanState tracks which of code / string / comment / regex we're
// currently inside, so `matchParen` and `splitTopLevelComma` don't
// count parens/commas that live in a non-code region. The `//`, `/* */`,
// and `/…/` awareness closes #277 round-2 ship-blockers #2-#4.
type jsScanState struct {
	// Exactly one of these is true when we're "inside" something.
	inSQuote, inDQuote, inBQuote bool
	inLineComment, inBlockComment bool
	inRegex, inRegexClass         bool
	// Last non-whitespace CODE byte we saw. Used to decide whether
	// `/` opens a regex literal or is division.
	prevSig byte
}

// step consumes byte s[i] and returns:
//   - codeByte: true if this byte is INSIDE code (i.e. the caller
//     should apply its per-byte logic like paren counting). False if
//     the byte is inside a literal / comment / regex.
//   - advance: number of ADDITIONAL bytes the caller should skip
//     past `i` (e.g. `//` consumes 2 bytes at once — return 1 so the
//     caller's `i++` plus this skips the second `/`).
func (st *jsScanState) step(s string, i int) (codeByte bool, advance int) {
	c := s[i]

	// ── inside line comment ──
	if st.inLineComment {
		if c == '\n' {
			st.inLineComment = false
			// The newline itself is code (used for line tracking).
			return true, 0
		}
		return false, 0
	}
	// ── inside block comment ──
	if st.inBlockComment {
		if c == '*' && i+1 < len(s) && s[i+1] == '/' {
			st.inBlockComment = false
			return false, 1
		}
		return false, 0
	}
	// ── inside single-quoted string ──
	if st.inSQuote {
		if c == '\\' && i+1 < len(s) {
			return false, 1
		}
		if c == '\'' {
			st.inSQuote = false
			st.prevSig = '\''
		}
		return false, 0
	}
	// ── inside double-quoted string ──
	if st.inDQuote {
		if c == '\\' && i+1 < len(s) {
			return false, 1
		}
		if c == '"' {
			st.inDQuote = false
			st.prevSig = '"'
		}
		return false, 0
	}
	// ── inside template literal ──
	if st.inBQuote {
		if c == '\\' && i+1 < len(s) {
			return false, 1
		}
		if c == '`' {
			st.inBQuote = false
			st.prevSig = '`'
		}
		return false, 0
	}
	// ── inside regex literal ──
	if st.inRegex {
		if c == '\\' && i+1 < len(s) {
			return false, 1
		}
		if st.inRegexClass {
			if c == ']' {
				st.inRegexClass = false
			}
			return false, 0
		}
		if c == '[' {
			st.inRegexClass = true
			return false, 0
		}
		if c == '/' {
			st.inRegex = false
			st.prevSig = '/'
		}
		return false, 0
	}

	// ── code: look for transitions ──
	// Line comment `//`
	if c == '/' && i+1 < len(s) && s[i+1] == '/' {
		st.inLineComment = true
		return false, 1
	}
	// Block comment `/*`
	if c == '/' && i+1 < len(s) && s[i+1] == '*' {
		st.inBlockComment = true
		return false, 1
	}
	// Regex literal `/…/` — context-sensitive: `/` starts a regex
	// when the previous non-whitespace code byte is an operator or
	// delimiter (or nothing). Conservative: treat as regex when
	// prevSig is a bounded set of chars. False positives here mean
	// we OVER-skip parens; false negatives mean we might miscount.
	// Real Supabase edge functions rarely mix division with parens
	// in ways that break this heuristic.
	if c == '/' && isRegexContext(st.prevSig) {
		st.inRegex = true
		return false, 0
	}
	// String literals
	switch c {
	case '\'':
		st.inSQuote = true
		return false, 0
	case '"':
		st.inDQuote = true
		return false, 0
	case '`':
		st.inBQuote = true
		return false, 0
	}
	// Regular code byte — update prevSig if it's meaningful.
	if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
		st.prevSig = c
	}
	return true, 0
}

// isRegexContext decides whether a `/` at the current position starts
// a regex literal (given the last non-whitespace CODE byte seen).
// A `/` after an operator or delimiter is regex; after an identifier
// or `)` or `]` it's division. `0` (start-of-input) is regex context.
func isRegexContext(prev byte) bool {
	switch prev {
	case 0: // start of input
		return true
	case '(', ',', '=', '!', '&', '|', '?', ':', '{', '[', ';', '<', '>', '+', '-', '*', '/', '%', '^', '~':
		return true
	}
	return false
}

// matchParen given the index of a `(` returns the index of its
// matching `)`. The scanner skips string literals (`'…'` / `"…"` /
// `` `…` ``), line comments (`// …`), block comments (`/* … */`),
// AND regex literals (`/…/`) — parens inside any of those don't
// count. Returns -1 on unbalanced input.
//
// Regex-literal disambiguation is context-sensitive: `/` starts a
// regex when it follows an operator / delimiter, and is division
// when it follows an identifier / number / `)`. See isRegexContext.
// (#277 round-2 ship-blockers #2-#4.)
func matchParen(s string, openIdx int) int {
	depth := 0
	var st jsScanState
	for i := openIdx; i < len(s); i++ {
		codeByte, advance := st.step(s, i)
		if advance > 0 {
			i += advance
			continue
		}
		if !codeByte {
			continue
		}
		switch s[i] {
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
	// `import type { … } from '…@supabase/supabase-js…'` — types-only
	// import, no runtime effect. Suppresses the SDK warning above.
	// (#277 round-2 review M #7.)
	reSupabaseSDKTypeImport = regexp.MustCompile(`(?m)^\s*import\s+type\b.*@supabase/supabase-js`)

	// `import { serve } from '…deno.land/std…/http/server.ts'` — the
	// canonical Supabase `functions new` template imports serve here
	// and calls `serve(handler)` at statement head. We rewrite those
	// like Deno.serve when this import is present. Guarded on the
	// import URL so we don't touch a tenant helper named `serve`.
	// (#277 round-2 ship-blocker #1.)
	reDenoStdServeImport = regexp.MustCompile(
		`(?m)^\s*import\s*\{[^}]*\bserve\b[^}]*\}\s*from\s*['"]https://deno\.land/std[^'"]*/http/server\.ts['"]\s*;?`,
	)
)
