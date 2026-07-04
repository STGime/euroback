package cli

// #269 (part of #267): Supabase → Eurobase DDL translator.
//
// Given a Supabase pg_dump output (schema-only, public schema),
// produces SQL that runs cleanly against a Eurobase tenant schema.
//
// This file is pure text transformation — no DB access, no shelling
// out. That's deliberate: it's the piece where subtle bugs (an
// over-eager regex clobbers a policy body, or worse, an atom rewrite
// runs inside a CREATE FUNCTION body) become hard-to-debug prod
// incidents, so it lives isolated and exhaustively tested.
//
// Architecture — two passes, both routed through a small SQL
// tokenizer (`splitCodeAndLiterals`). The tokenizer separates code
// from string literals, dollar-quoted function bodies, line and
// block comments, and double-quoted identifiers. Rewrites and
// statement-splitting only act on code segments; literal segments
// pass through unchanged.
//
//   1. Pass 1 — line-by-line strips (SET, CREATE SCHEMA public, …)
//      and atom rewrites (auth.uid → auth_uid, public.<table> →
//      <table>, extensions.uuid_generate_v4 → public.uuid_generate_v4,
//      …). Strip rules apply only to source lines that are ENTIRELY
//      code (no straddling into a literal on the same line — that
//      case is handled by keeping the line and only rewriting the
//      code portions). This is what fixes S2/H1 from the #274
//      re-review: `INSERT INTO events VALUES ('table public.orders')`
//      comes out unchanged, and `RAISE NOTICE 'auth.uid() = %'`
//      inside a dollar-quoted function body is left alone.
//
//   2. Pass 2 — statement-level: splits on `;` (respecting the
//      tokenizer, so `;` inside dollar-quoted bodies doesn't split
//      the statement) and wraps every CREATE POLICY body in
//      `public.is_service_role() OR (...)`. This is what fixes B2
//      from the first review (pg_dump pretty-prints long policy
//      bodies across multiple lines) and S1 from the re-review
//      (a `;` inside a `$$…$$` body would otherwise false-split
//      the statement).

import (
	"regexp"
	"strings"
)

// translationWarning records one thing the translator noticed but
// couldn't safely rewrite. Surfaced back to the caller so the CLI can
// print them all before writing the output file — the tenant sees a
// visible "check these before applying" list.
type translationWarning struct {
	line int    // 1-indexed line in the input
	note string // human-readable
}

// translationResult is what Translate returns.
type translationResult struct {
	sql      string
	warnings []translationWarning
	// rewriteCount tracks how many of each rewrite fired. Useful for
	// tests and for a "changed N policies" summary line at CLI exit.
	rewrites map[string]int
}

// Translate rewrites a Supabase pg_dump schema-only body for Eurobase.
// See the file-level doc for the two-pass architecture.
func Translate(input string) translationResult {
	res := translationResult{rewrites: map[string]int{}}

	// Tokenizer view: byte i is code iff mask[i]. Used by Pass 1 to
	// avoid rewriting inside literals; Pass 2 re-tokenizes since it
	// walks statements rather than lines.
	mask, unterminated := buildCodeMask(input)
	if unterminated {
		// A truncated pg_dump or a malformed hand-authored file will
		// hit EOF inside a string / dollar-quote / block-comment. The
		// tokenizer's fallback (treat the tail as one literal) protects
		// against silent atom-rewrites, but the tenant needs to know
		// the output is likely to fail at apply time.
		res.warnings = append(res.warnings, translationWarning{
			line: 0,
			note: "input contains an unterminated string / dollar-quote / block-comment — the SQL is truncated or malformed; the emitted file will likely fail at apply time",
		})
	}

	// ── Pass 1: source-line by source-line ──
	pass1 := translatePass1(input, mask, &res)

	// ── Pass 2: statement-level policy wrap ──
	final, wraps := wrapPoliciesInStatements(pass1)
	if wraps > 0 {
		res.rewrites["rewrite:policy body OR'd with is_service_role()"] += wraps
	}
	res.sql = final
	return res
}

// translatePass1 walks the input line by line. A line ends at a `\n`
// that appears in the source. For each line:
//
//   - If the entire line is code (mask says so for every byte), try
//     strip rules on the raw line. If matched, drop it.
//   - Otherwise, apply atom rewrites only to code spans of the line.
//     Literal spans pass through byte-for-byte.
//   - Warnings check the rewritten line.
func translatePass1(input string, mask []bool, res *translationResult) string {
	var out strings.Builder
	out.Grow(len(input))
	lineNum := 1
	lineStart := 0
	n := len(input)
	for i := 0; i <= n; i++ {
		if i < n && input[i] != '\n' {
			continue
		}
		line := input[lineStart:i]
		lineMask := mask[lineStart:i]

		// Strip rules match on the KEYWORD prefix (SET / CREATE SCHEMA /
		// ALTER SCHEMA / COMMENT ON SCHEMA). A line like
		// `SET client_encoding = 'UTF8';` has a literal at the tail but
		// the SET keyword itself is code — safe to strip the whole line.
		// A line like `    SET x = 1;` inside a dollar-quoted function
		// body would ALSO textually start with SET, but the tokenizer
		// mask says the SET bytes are inside a literal — don't strip.
		//
		// We gate on the mask at the first non-whitespace byte: that
		// tells us whether the "real" statement head lives in code.
		headIsCode := true
		for j := 0; j < len(line); j++ {
			if line[j] != ' ' && line[j] != '\t' {
				headIsCode = lineMask[j]
				break
			}
		}

		dropped := false
		if headIsCode {
			trimmed := strings.TrimLeft(line, " \t")
			switch {
			case reSetAny.MatchString(trimmed):
				res.rewrites["strip:SET <preamble>"]++
				dropped = true
			case reCreateSchemaPublic.MatchString(trimmed):
				res.rewrites["strip:CREATE SCHEMA public"]++
				dropped = true
			case reAlterSchemaOwner.MatchString(trimmed),
				reCommentOnSchema.MatchString(trimmed):
				res.rewrites["strip:schema meta"]++
				dropped = true
			}
		}

		if !dropped {
			rewritten, changed, warns := translateCodeSpans(line, lineMask, lineNum)
			for k, v := range changed {
				res.rewrites[k] += v
			}
			res.warnings = append(res.warnings, warns...)
			out.WriteString(rewritten)
			if i < n {
				out.WriteByte('\n')
			}
		} else if i < n {
			// Dropped line: also drop its trailing newline so the file
			// doesn't grow blank lines where the preamble used to be.
			// (Matches the previous line-by-line behaviour.)
		}

		if i < n {
			lineNum++
		}
		lineStart = i + 1
	}
	return out.String()
}

// translateCodeSpans applies atom rewrites to the code spans of a
// single source line, leaving literal spans byte-for-byte identical.
// Returns the rewritten line, the per-rule fire counts, and any
// warnings that fired.
func translateCodeSpans(line string, mask []bool, lineNum int) (string, map[string]int, []translationWarning) {
	fired := map[string]int{}
	var warns []translationWarning
	if len(line) == 0 {
		return line, fired, warns
	}

	// Split into runs of code / literal based on mask.
	type run struct {
		text string
		code bool
	}
	var runs []run
	curStart := 0
	curCode := mask[0]
	for i := 1; i < len(line); i++ {
		if mask[i] != curCode {
			runs = append(runs, run{line[curStart:i], curCode})
			curStart = i
			curCode = mask[i]
		}
	}
	runs = append(runs, run{line[curStart:], curCode})

	// Apply rewrites to code runs only. Order matters:
	//   - storage.objects → storage_objects before any REFERENCES rewrite
	//   - auth.users → users before auth.<func>()
	//   - extensions.* rewrites before the "custom schema" warning
	//     (which no longer lists `extensions`)
	//   - public.<table> stripping runs last — it's context-gated on
	//     a preceding DDL keyword within the same run
	for idx := range runs {
		if !runs[idx].code {
			continue
		}
		text := runs[idx].text
		if n := countAndReplace(&text, reStorageObjects, "storage_objects"); n > 0 {
			fired["rewrite:storage.objects → storage_objects"] += n
		}
		if n := countAndReplace(&text, reAuthUsers, "users"); n > 0 {
			fired["rewrite:auth.users → users"] += n
		}
		if n := countAndReplace(&text, reAuthUID, "auth_uid()"); n > 0 {
			fired["rewrite:auth.uid() → auth_uid()"] += n
		}
		if n := countAndReplace(&text, reAuthRole,
			"(CASE WHEN public.is_service_role() THEN 'service_role' ELSE 'authenticated' END)"); n > 0 {
			fired["rewrite:auth.role() → CASE"] += n
		}
		if n := countAndReplace(&text, reAuthEmail, "auth_email()"); n > 0 {
			fired["rewrite:auth.email() → auth_email()"] += n
		}
		if n := countAndReplace(&text, reExtensionsUUID, "public.uuid_generate_v4()"); n > 0 {
			fired["rewrite:extensions.uuid_generate_v4() → public.uuid_generate_v4()"] += n
		}
		if n := countAndReplace(&text, reExtensionsGenRandomUUID, "gen_random_uuid()"); n > 0 {
			fired["rewrite:extensions.gen_random_uuid() → gen_random_uuid()"] += n
		}
		if n := countAndReplacePublicQualifier(&text); n > 0 {
			fired["rewrite:public.<table> → <table>"] += n
		}
		runs[idx].text = text
	}

	var lineBuilder strings.Builder
	lineBuilder.Grow(len(line))
	for _, r := range runs {
		lineBuilder.WriteString(r.text)
	}
	out := lineBuilder.String()

	// Warnings — check the code portions of the rewritten line only, so
	// a legitimate `auth.jwt()` string inside a dollar-quoted body or
	// a comment doesn't fire a spurious "you use auth.jwt()" warning.
	// Cheap approach: concatenate only the code runs and match there.
	var codeOnly strings.Builder
	for _, r := range runs {
		if r.code {
			codeOnly.WriteString(r.text)
		}
	}
	code := codeOnly.String()
	if reAuthJWT.MatchString(code) {
		warns = append(warns, translationWarning{
			line: lineNum,
			note: "uses `auth.jwt()` — Eurobase does not expose the raw JWT to SQL; verify the intent (may need a policy rewrite that reads Eurobase's session GUC instead)",
		})
	}
	if reCustomSchemaRef.MatchString(code) {
		warns = append(warns, translationWarning{
			line: lineNum,
			note: "references a schema other than public/tenant/storage — extensions in custom schemas may not exist post-migration (check the assess report)",
		})
	}

	return out, fired, warns
}

// wrapPoliciesInStatements walks the input by SQL statements — a
// statement ends at the next `;` that lives in a code segment. For
// each statement whose head reads CREATE POLICY, wraps every USING
// and WITH CHECK body in `public.is_service_role() OR (...)`.
//
// Tokenizer-driven: a `;` inside a dollar-quoted function body or a
// single-quoted string doesn't split the statement. This is the fix
// for S1 from the #274 re-review — the old byte-loop split
// `CREATE FUNCTION foo() AS $$ … PERFORM 1; PERFORM 2; … $$` at the
// first inner `;`.
func wrapPoliciesInStatements(sql string) (string, int) {
	segments, _ := splitCodeAndLiterals(sql)
	var out strings.Builder
	out.Grow(len(sql))
	var stmt strings.Builder
	total := 0
	flush := func() {
		if stmt.Len() == 0 {
			return
		}
		s := stmt.String()
		trimmed := strings.TrimLeft(s, " \t\n\r")
		if reCreatePolicyMulti.MatchString(trimmed) {
			wrapped, n := wrapPolicyBody(s)
			out.WriteString(wrapped)
			total += n
		} else {
			out.WriteString(s)
		}
		stmt.Reset()
	}
	for _, seg := range segments {
		if !seg.code {
			stmt.WriteString(seg.text)
			continue
		}
		text := seg.text
		for {
			idx := strings.IndexByte(text, ';')
			if idx == -1 {
				stmt.WriteString(text)
				break
			}
			stmt.WriteString(text[:idx+1])
			flush()
			text = text[idx+1:]
		}
	}
	flush()
	return out.String(), total
}

// wrapPolicyBody rewrites `USING (X)` → `USING (public.is_service_role() OR (X))`
// and the same for `WITH CHECK (X)`. Requires balanced parens (regex
// finds the opening paren; we walk to the matching close). Returns
// the rewritten statement and the count of clauses wrapped.
//
// Idempotent: a body that already contains `public.is_service_role() OR (`
// is left alone (avoids double-wrapping when the CLI is rerun on
// already-translated input).
func wrapPolicyBody(stmt string) (string, int) {
	if strings.Contains(stmt, "public.is_service_role() OR (") {
		return stmt, 0
	}
	changed := 0
	for _, clauseKW := range []string{"USING", "WITH CHECK"} {
		// Case-insensitive keyword search, byte-aligned to the original
		// so paren offsets stay valid.
		lower := strings.ToLower(stmt)
		kw := strings.ToLower(clauseKW)
		idx := 0
		for {
			hit := strings.Index(lower[idx:], kw)
			if hit == -1 {
				break
			}
			hit += idx
			openParen := strings.IndexByte(stmt[hit:], '(')
			if openParen == -1 {
				break
			}
			openParen += hit
			closeParen := findMatchingParen(stmt, openParen)
			if closeParen == -1 {
				break
			}
			body := stmt[openParen+1 : closeParen]
			if strings.Contains(body, "public.is_service_role() OR (") {
				idx = closeParen + 1
				continue
			}
			wrapped := "public.is_service_role() OR (" + body + ")"
			stmt = stmt[:openParen+1] + wrapped + stmt[closeParen:]
			changed++
			lower = strings.ToLower(stmt)
			idx = openParen + 1 + len(wrapped) + 1
		}
	}
	return stmt, changed
}

// findMatchingParen given the index of a `(` returns the index of its
// matching `)`. Uses the SQL tokenizer to ignore parens inside string
// literals, dollar-quoted bodies, and comments — a policy body like
// `USING (col = 'foo(bar)')` or one that contains an inline block
// comment `/* (unused) */` won't corrupt the walker.
func findMatchingParen(s string, openIdx int) int {
	segments, _ := splitCodeAndLiterals(s[openIdx:])
	depth := 0
	offset := openIdx
	for _, seg := range segments {
		if !seg.code {
			offset += len(seg.text)
			continue
		}
		for j := 0; j < len(seg.text); j++ {
			switch seg.text[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return offset + j
				}
			}
		}
		offset += len(seg.text)
	}
	return -1
}

// ── tokenizer ────────────────────────────────────────────────────────

// segment is one contiguous run of either SQL code or a single
// literal (string / dollar-quoted body / comment / quoted identifier).
type segment struct {
	text string
	code bool
}

// splitCodeAndLiterals walks a Postgres SQL string and returns
// segments, each either code or a single literal, along with a bool
// that's true if the tokenizer hit EOF while still inside a literal
// (an unterminated string / dollar-quote / block-comment). Handles:
//
//   - Single-quoted strings: `'foo''bar'` (doubled `''` is an escape).
//   - Escape-string literals: `E'foo\'bar\n'` — the `E`/`e` prefix
//     honors backslash escapes (`\'`, `\\`, `\n`, `\t`, etc.). Without
//     this, `E'a\'b public.orders'` would false-close at the `\'` and
//     Pass 1 would rewrite `public.orders` in the tail. pg_dump emits
//     E-strings whenever a string contains a backslash, so this is a
//     real (not theoretical) corruption path — #274 re-review H-a.
//   - Dollar-quoted strings: `$$…$$` or `$tag$…$tag$` (per PG spec, the
//     tag identifier must start with a letter or underscore — a bare
//     `$1` is a parameter placeholder, not an opener).
//   - Line comments: `-- …` up to but not including the newline.
//   - Block comments: `/* … */` — Postgres allows nesting, unlike
//     the SQL standard (see PG docs §4.1.5).
//   - Double-quoted identifiers: `"foo""bar"` (also treated as literal
//     so a hostile identifier like `"public.orders"` doesn't invite
//     the atom rewrites to touch it).
//
// This is the safety net for the #274 re-review's S1/S2/H1/H-a
// findings.
func splitCodeAndLiterals(sql string) ([]segment, bool) {
	var out []segment
	unterminated := false
	n := len(sql)
	codeStart := 0
	flushCode := func(upto int) {
		if upto > codeStart {
			out = append(out, segment{sql[codeStart:upto], true})
		}
	}
	i := 0
	for i < n {
		c := sql[i]

		// -- line comment
		if c == '-' && i+1 < n && sql[i+1] == '-' {
			flushCode(i)
			nl := strings.IndexByte(sql[i:], '\n')
			if nl == -1 {
				// Line comment through EOF is well-formed (no newline
				// required); don't flag as unterminated.
				out = append(out, segment{sql[i:], false})
				return out, unterminated
			}
			out = append(out, segment{sql[i : i+nl], false})
			i += nl
			codeStart = i
			continue
		}

		// /* block comment */ (nested)
		if c == '/' && i+1 < n && sql[i+1] == '*' {
			flushCode(i)
			start := i
			i += 2
			depth := 1
			for i < n && depth > 0 {
				switch {
				case i+1 < n && sql[i] == '/' && sql[i+1] == '*':
					depth++
					i += 2
				case i+1 < n && sql[i] == '*' && sql[i+1] == '/':
					depth--
					i += 2
				default:
					i++
				}
			}
			if depth > 0 {
				unterminated = true
			}
			out = append(out, segment{sql[start:i], false})
			codeStart = i
			continue
		}

		// dollar-quoted string
		if c == '$' {
			if tag, tagLen, ok := parseDollarTag(sql, i); ok {
				flushCode(i)
				start := i
				bodyStart := i + tagLen
				closeRel := strings.Index(sql[bodyStart:], tag)
				if closeRel == -1 {
					// Unterminated — treat rest as literal so we
					// don't accidentally rewrite half of it, and
					// flag so Translate can warn.
					unterminated = true
					out = append(out, segment{sql[start:], false})
					return out, unterminated
				}
				i = bodyStart + closeRel + tagLen
				out = append(out, segment{sql[start:i], false})
				codeStart = i
				continue
			}
		}

		// E'…' / e'…' escape-string literal. Detect by looking back:
		// the `'` at position i is an E-string opener iff the byte
		// immediately before is `E`/`e` AND the byte before that is
		// not part of an identifier (start-of-input or a non-ident
		// char). Adjacent — Postgres does not allow whitespace between
		// the prefix and the `'`.
		if c == '\'' && i >= 1 && (sql[i-1] == 'E' || sql[i-1] == 'e') &&
			(i == 1 || !isTagChar(sql[i-2])) {
			flushCode(i - 1)
			start := i - 1
			i++ // past the '
			terminated := false
			for i < n {
				if sql[i] == '\\' {
					// Two-byte escape (backslash + any char).
					if i+1 < n {
						i += 2
						continue
					}
					// Trailing backslash — treat as single byte.
					i++
					continue
				}
				if sql[i] == '\'' {
					if i+1 < n && sql[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					terminated = true
					break
				}
				i++
			}
			if !terminated {
				unterminated = true
			}
			out = append(out, segment{sql[start:i], false})
			codeStart = i
			continue
		}

		// single-quoted string (no backslash escape processing —
		// standard_conforming_strings=on is the modern default and
		// pg_dump respects it).
		if c == '\'' {
			flushCode(i)
			start := i
			i++
			terminated := false
			for i < n {
				if sql[i] == '\'' {
					if i+1 < n && sql[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					terminated = true
					break
				}
				i++
			}
			if !terminated {
				unterminated = true
			}
			out = append(out, segment{sql[start:i], false})
			codeStart = i
			continue
		}

		// double-quoted identifier
		if c == '"' {
			flushCode(i)
			start := i
			i++
			terminated := false
			for i < n {
				if sql[i] == '"' {
					if i+1 < n && sql[i+1] == '"' {
						i += 2
						continue
					}
					i++
					terminated = true
					break
				}
				i++
			}
			if !terminated {
				unterminated = true
			}
			out = append(out, segment{sql[start:i], false})
			codeStart = i
			continue
		}

		i++
	}
	flushCode(n)
	return out, unterminated
}

// buildCodeMask returns a per-byte true/false view of `sql`: mask[i] is
// true iff position i is inside a code segment. Also returns a bool
// that's true if the tokenizer hit EOF inside a literal — surfaced by
// Translate as a warning so the tenant knows their input was truncated.
func buildCodeMask(sql string) ([]bool, bool) {
	mask := make([]bool, len(sql))
	segments, unterminated := splitCodeAndLiterals(sql)
	offset := 0
	for _, seg := range segments {
		for j := 0; j < len(seg.text); j++ {
			mask[offset+j] = seg.code
		}
		offset += len(seg.text)
	}
	return mask, unterminated
}

// parseDollarTag detects a valid dollar-quote opener at sql[i]. Returns
// the full tag (e.g. `$$` or `$body$`), its byte length, and true on
// success. Returns false for things like `$1` (positional parameter),
// bare `$foo` without a closing `$`, or EOF.
func parseDollarTag(sql string, i int) (string, int, bool) {
	if i >= len(sql) || sql[i] != '$' {
		return "", 0, false
	}
	if i+1 < len(sql) && sql[i+1] == '$' {
		return "$$", 2, true
	}
	j := i + 1
	if j >= len(sql) || !isTagStart(sql[j]) {
		return "", 0, false
	}
	for j < len(sql) && isTagChar(sql[j]) {
		j++
	}
	if j >= len(sql) || sql[j] != '$' {
		return "", 0, false
	}
	return sql[i : j+1], j + 1 - i, true
}

func isTagStart(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z')
}

func isTagChar(c byte) bool {
	return isTagStart(c) || (c >= '0' && c <= '9')
}

// countAndReplace does a `ReplaceAllString` on `*s` and returns the
// number of matches replaced. Small helper so translateCodeSpans can
// keep its per-rewrite bookkeeping cheap.
func countAndReplace(s *string, re *regexp.Regexp, repl string) int {
	matches := re.FindAllStringIndex(*s, -1)
	if len(matches) == 0 {
		return 0
	}
	*s = re.ReplaceAllString(*s, repl)
	return len(matches)
}

// countAndReplacePublicQualifier strips `public.` from table/sequence
// references while leaving `public.<func>()` calls, string literals,
// and USING-clause operator classes intact. pg_dump emits `CREATE
// TABLE public.orders`, `REFERENCES public.users(id)`, `ALTER TABLE
// ONLY public.orders`, `JOIN public.orders`, `DROP TABLE IF EXISTS
// public.orders`, etc.; Eurobase's tenant migrator pins `search_path`
// to the tenant schema, so an explicit `public.` qualifier lands the
// object in `public` (which the per-tenant `_ddl` role can't create
// in). We keep `public.uuid_generate_v4()` and
// `public.is_service_role()` intact — those are function calls in
// Eurobase's public schema and must stay qualified. Gated on the
// preceding DDL keyword (see rePublicQualifierContext). Since the
// caller already limited the input to code-only spans, string-literal
// false positives (`'public.foo'::regclass`) are impossible.
func countAndReplacePublicQualifier(s *string) int {
	matches := rePublicQualifierContext.FindAllStringIndex(*s, -1)
	if len(matches) == 0 {
		return 0
	}
	*s = rePublicQualifierContext.ReplaceAllString(*s, "$1")
	return len(matches)
}

// ── regex table ──────────────────────────────────────────────────────

var (
	// `SET <ident> …` — pg_dump's preamble is a run of these
	// (`SET statement_timeout`, `SET client_encoding`,
	// `SET search_path`, `SET row_security`,
	// `SET default_table_access_method`, …). All get stripped: the
	// Eurobase migration executor's session already has the correct
	// search_path pinned + row_security enforced, and stray SETs
	// would either fail on the migrator role or silently disable
	// enforcement for the tx.
	//
	// Also fires on `SET LOCAL/SESSION/ROLE/TIME ZONE …` — all of
	// which are safe to strip in tenant-migration context (Eurobase
	// controls the session role and locale). The wider net is
	// intentional. (#274 review M2, M6.)
	reSetAny             = regexp.MustCompile(`(?i)^\s*SET\s+\w+`)
	reCreateSchemaPublic = regexp.MustCompile(`(?i)^\s*CREATE\s+SCHEMA\s+(IF\s+NOT\s+EXISTS\s+)?public\b`)
	reAlterSchemaOwner   = regexp.MustCompile(`(?i)^\s*ALTER\s+SCHEMA\s+\w+\s+OWNER\s+TO`)
	reCommentOnSchema    = regexp.MustCompile(`(?i)^\s*COMMENT\s+ON\s+SCHEMA\b`)

	// `auth.users` as a table reference. Word-boundaries at each end
	// so `auth.users_view` doesn't get mangled.
	reAuthUsers = regexp.MustCompile(`\bauth\.users\b`)

	// `auth.uid()` / `auth.role()` / `auth.email()`. Trailing `()`
	// pinned to distinguish from column refs like `auth.uid` (nobody
	// writes those but doesn't hurt).
	reAuthUID   = regexp.MustCompile(`\bauth\.uid\s*\(\s*\)`)
	reAuthRole  = regexp.MustCompile(`\bauth\.role\s*\(\s*\)`)
	reAuthEmail = regexp.MustCompile(`\bauth\.email\s*\(\s*\)`)

	// `auth.jwt()` — warn only (Eurobase doesn't expose JWT to SQL).
	reAuthJWT = regexp.MustCompile(`\bauth\.jwt\s*\(\s*\)`)

	// `storage.objects` as a table reference.
	reStorageObjects = regexp.MustCompile(`\bstorage\.objects\b`)

	// `extensions.uuid_generate_v4()` / `.gen_random_uuid()` — Supabase
	// installs the uuid-ossp/pgcrypto helpers into the `extensions`
	// schema; Eurobase exposes them at `public.uuid_generate_v4()` and
	// as the built-in `gen_random_uuid()`. (#274 review H4.)
	reExtensionsUUID          = regexp.MustCompile(`\bextensions\.uuid_generate_v4\s*\(\s*\)`)
	reExtensionsGenRandomUUID = regexp.MustCompile(`\bextensions\.gen_random_uuid\s*\(\s*\)`)

	// `CREATE POLICY` at statement head — used by Pass 2 to decide
	// which statements to run through wrapPolicyBody.
	reCreatePolicyMulti = regexp.MustCompile(`(?i)^\s*CREATE\s+POLICY\b`)

	// `<DDL keyword> public.` — used by countAndReplacePublicQualifier.
	// We strip `public.` only after keywords that unambiguously precede
	// a table/sequence/view/trigger name in pg_dump's output:
	//
	//   TABLE / INTO / FROM / JOIN / UPDATE / REFERENCES / ON
	//   / SEQUENCE / VIEW / TRIGGER / OWNED BY
	//
	// Plus optional `ONLY`, `IF NOT EXISTS`, or `IF EXISTS` modifiers
	// between the keyword and the qualifier. This gate is deliberately
	// narrower than "any `public.<ident>`" — a blanket strip would
	// corrupt function calls (`public.uuid_generate_v4()`) and USING-
	// clause operator classes. String-literal safety is handled by
	// the tokenizer at the layer above. (#274 review B1 + H2 + H3.)
	rePublicQualifierContext = regexp.MustCompile(
		`(?i)(\b(?:TABLE|INTO|FROM|JOIN|UPDATE|REFERENCES|ON|SEQUENCE|VIEW|TRIGGER|OWNED\s+BY)\s+(?:ONLY\s+|IF\s+NOT\s+EXISTS\s+|IF\s+EXISTS\s+)?)public\.`,
	)

	// Any qualified reference to a schema that ISN'T public / auth /
	// storage / pg_catalog / information_schema / tenant (the ones
	// we've already handled or intentionally left alone). Best-effort
	// warning — false-positives cost the tenant a look, a false-
	// negative would cost them a broken migration. `extensions` is
	// not in this list because we rewrite the common helpers on that
	// schema; anything else on `extensions` gets flagged separately
	// if it's a schema Supabase-only tenants would rely on.
	reCustomSchemaRef = regexp.MustCompile(`\b(?:graphql|graphql_public|realtime|vault|net|cron|supabase_functions|_supabase|pgmq_public)\.\w+`)
)
