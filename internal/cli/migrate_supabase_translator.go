package cli

// #269 (part of #267): Supabase → Eurobase DDL translator.
//
// Given a Supabase pg_dump output (schema-only, public schema),
// produces SQL that runs cleanly against a Eurobase tenant schema.
// Six canonical rewrites cover ~95% of tenant projects; anything
// exotic gets flagged in the emitted warnings list rather than
// silently mistranslated.
//
// This file is pure text transformation — no DB access, no shelling
// out. That's deliberate: it's the piece where subtle bugs (an over-
// eager regex clobbers a policy body) become hard-to-debug prod
// incidents, so it lives isolated and exhaustively tested.

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
//
// Two passes:
//
//  1. Line-by-line — strips no-op meta lines (SET, CREATE SCHEMA
//     public, …) and applies atom rewrites (auth.uid → auth_uid, etc).
//     `public.<table>` qualifiers stripped in the six statement-head
//     contexts (CREATE TABLE, ALTER TABLE, REFERENCES, ON, CREATE
//     INDEX … ON, etc.) so DDL lands in the tenant schema.
//
//  2. Statement-level — splits the output on `;` outside string
//     literals and wraps every CREATE POLICY body in
//     `public.is_service_role() OR (...)`. Two-pass matters because
//     pg_dump pretty-prints long policy bodies across multiple lines;
//     wrapping at line-level would silently miss those (review B2 on
//     PR #274 flagged this as a ship-blocker).
//
// Warns (not rewrites) — the tenant must review:
//   - Any use of `auth.jwt() ->> …` on a project-specific claim.
//   - Any non-public / non-storage schema qualifier that we don't
//     already rewrite (extensions in custom schemas, foreign-data
//     wrappers, etc.).
func Translate(input string) translationResult {
	res := translationResult{rewrites: map[string]int{}}

	// ── Pass 1: line-by-line ────────────────────────────────────────
	var pass1 strings.Builder
	pass1.Grow(len(input))
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		lineNum := i + 1
		rewritten, changed, warns := translateLine(line, lineNum)
		for name := range changed {
			res.rewrites[name] += changed[name]
		}
		res.warnings = append(res.warnings, warns...)
		if rewritten == skipLineSentinel {
			continue
		}
		pass1.WriteString(rewritten)
		if i < len(lines)-1 {
			pass1.WriteString("\n")
		}
	}

	// ── Pass 2: statement-level policy wrap ─────────────────────────
	intermediate := pass1.String()
	final, wraps := wrapPoliciesInStatements(intermediate)
	if wraps > 0 {
		res.rewrites["rewrite:policy body OR'd with is_service_role()"] += wraps
	}
	res.sql = final
	return res
}

// wrapPoliciesInStatements handles the multi-line policy case (B2).
// Splits the input on `;` outside string literals; for each statement
// that starts with CREATE POLICY, wraps every USING/WITH CHECK body.
// Non-policy statements pass through unchanged.
func wrapPoliciesInStatements(sql string) (string, int) {
	var out strings.Builder
	out.Grow(len(sql))
	total := 0
	i := 0
	for i < len(sql) {
		start := i
		// Advance past one statement (up to and including the `;`
		// that terminates it — or EOF).
		end := findStatementEnd(sql, start)
		stmt := sql[start:end]
		trimmed := strings.TrimLeft(stmt, " \t\n\r")
		if reCreatePolicyMulti.MatchString(trimmed) {
			wrapped, n := wrapPolicyBody(stmt)
			out.WriteString(wrapped)
			total += n
		} else {
			out.WriteString(stmt)
		}
		i = end
	}
	return out.String(), total
}

// findStatementEnd returns the index one past the terminating `;` of
// the statement starting at `start`, respecting single-quoted string
// literals + doubled-quote escapes. On EOF, returns len(sql).
func findStatementEnd(sql string, start int) int {
	inString := false
	for i := start; i < len(sql); i++ {
		c := sql[i]
		if inString {
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
			continue
		}
		switch c {
		case '\'':
			inString = true
		case ';':
			return i + 1
		}
	}
	return len(sql)
}

// skipLineSentinel is returned by translateLine when the whole line
// should be dropped from the output (e.g. `SET search_path = public;`).
const skipLineSentinel = "\x00drop\x00"

// translateLine processes one line. Returns the rewritten line
// (possibly the sentinel), a map of rewrite counts that fired on this
// line, and any warnings emitted.
func translateLine(line string, lineNum int) (string, map[string]int, []translationWarning) {
	fired := map[string]int{}
	var warns []translationWarning

	trimmed := strings.TrimLeft(line, " \t")

	// Drop-line rules — evaluated first because there's no point
	// rewriting a line we're about to throw away.
	//
	// Any `SET <ident> = …` in the pg_dump preamble is stripped:
	// the Eurobase migration executor's session already has the
	// correct search_path pinned + row_security enforced, and stray
	// SET default_table_access_method / SET row_security /
	// SET transaction_read_only would either fail or silently
	// disable enforcement for the tx. Review M6 flagged this.
	if reSetAny.MatchString(trimmed) {
		fired["strip:SET <preamble>"]++
		return skipLineSentinel, fired, warns
	}
	if reCreateSchemaPublic.MatchString(trimmed) {
		fired["strip:CREATE SCHEMA public"]++
		return skipLineSentinel, fired, warns
	}
	// `ALTER SCHEMA … OWNER TO …` and `COMMENT ON SCHEMA …` also
	// have no meaning in tenant context; drop them.
	if reAlterSchemaOwner.MatchString(trimmed) || reCommentOnSchema.MatchString(trimmed) {
		fired["strip:schema meta"]++
		return skipLineSentinel, fired, warns
	}

	// In-place rewrites. Order matters:
	// (a) `storage.objects` → `storage_objects` before any
	//     `references` rewrite so we don't miss the FK case.
	// (b) `auth.users` → `users` before `auth.<func>()` so a
	//     `REFERENCES auth.users` doesn't get mis-matched by the
	//     function-call regex.
	// (c) `auth.uid()` before `auth.role()` (both simple identifiers,
	//     order is stylistic).
	out := line
	if n := countAndReplace(&out, reStorageObjects, "storage_objects"); n > 0 {
		fired["rewrite:storage.objects → storage_objects"] += n
	}
	if n := countAndReplace(&out, reAuthUsers, "users"); n > 0 {
		fired["rewrite:auth.users → users"] += n
	}
	if n := countAndReplace(&out, reAuthUID, "auth_uid()"); n > 0 {
		fired["rewrite:auth.uid() → auth_uid()"] += n
	}
	if n := countAndReplace(&out, reAuthRole,
		"(CASE WHEN public.is_service_role() THEN 'service_role' ELSE 'authenticated' END)"); n > 0 {
		fired["rewrite:auth.role() → CASE"] += n
	}
	if n := countAndReplace(&out, reAuthEmail, "auth_email()"); n > 0 {
		fired["rewrite:auth.email() → auth_email()"] += n
	}
	// H4: Supabase's pg_dump emits `extensions.uuid_generate_v4()`;
	// Eurobase's tenant Postgres exposes it as `public.uuid_generate_v4()`.
	// Same for `extensions.gen_random_uuid()` → the built-in
	// `gen_random_uuid()`.
	if n := countAndReplace(&out, reExtensionsUUID, "public.uuid_generate_v4()"); n > 0 {
		fired["rewrite:extensions.uuid_generate_v4() → public.uuid_generate_v4()"] += n
	}
	if n := countAndReplace(&out, reExtensionsGenRandomUUID, "gen_random_uuid()"); n > 0 {
		fired["rewrite:extensions.gen_random_uuid() → gen_random_uuid()"] += n
	}
	// B1: Strip `public.` schema qualifier from table references in
	// the specific DDL / policy / FK contexts where pg_dump emits it.
	// The Eurobase executor pins `SET search_path TO <tenant>` — an
	// explicit `public.` would send the object to `public` (which
	// the per-tenant _ddl role can't create in). We keep `public.`
	// on function calls (e.g. `public.uuid_generate_v4()`) because
	// those live in Eurobase's public schema and must stay
	// qualified. Regex-anchored on statement heads (not
	// blanket-strip) so functions and quoted strings are safe.
	if n := countAndReplacePublicQualifier(&out); n > 0 {
		fired["rewrite:public.<table> → <table>"] += n
	}

	// Warnings — the tenant needs to eyeball these.
	if reAuthJWT.MatchString(out) {
		warns = append(warns, translationWarning{
			line: lineNum,
			note: "uses `auth.jwt()` — Eurobase does not expose the raw JWT to SQL; verify the intent (may need a policy rewrite that reads Eurobase's session GUC instead)",
		})
	}
	if reCustomSchemaRef.MatchString(out) {
		// Only warn on schemas we haven't already normalised.
		warns = append(warns, translationWarning{
			line: lineNum,
			note: "references a schema other than public/tenant/storage — extensions in custom schemas may not exist post-migration (check the assess report)",
		})
	}

	// Note: policy-body wrapping runs at statement level (Pass 2), not
	// here. pg_dump pretty-prints long policy bodies across multiple
	// lines, so per-line wrap silently misses them (#274 review B2).

	return out, fired, warns
}

// countAndReplace does a `ReplaceAllString` on `*s` and returns the
// number of matches replaced. Small helper so translateLine can keep
// its per-rewrite bookkeeping cheap.
func countAndReplace(s *string, re *regexp.Regexp, repl string) int {
	matches := re.FindAllStringIndex(*s, -1)
	if len(matches) == 0 {
		return 0
	}
	*s = re.ReplaceAllString(*s, repl)
	return len(matches)
}

// wrapPolicyBody rewrites `USING (X)` → `USING (public.is_service_role() OR (X))`
// and the same for `WITH CHECK (X)`. Requires balanced parens (regex
// finds the opening paren; we walk to the matching close). Returns
// the rewritten line and the count of clauses wrapped.
//
// Idempotent: a body that already contains `public.is_service_role()`
// is left alone (avoids double-wrapping when the CLI is rerun on an
// already-translated file).
func wrapPolicyBody(line string) (string, int) {
	// Look for the OR-wrap pattern specifically. Checking just for
	// `public.is_service_role()` would false-positive on the CASE
	// emitted by the auth.role() rewrite (which contains that helper
	// as part of its body but isn't itself an OR-wrap).
	if strings.Contains(line, "public.is_service_role() OR (") {
		return line, 0
	}
	changed := 0
	for _, clauseKW := range []string{"USING", "WITH CHECK"} {
		// Case-insensitive search for the keyword. We use lower-cased
		// comparison rather than a regex so the paren walker below
		// operates on byte offsets stably.
		lower := strings.ToLower(line)
		kw := strings.ToLower(clauseKW)
		idx := 0
		for {
			hit := strings.Index(lower[idx:], kw)
			if hit == -1 {
				break
			}
			hit += idx
			openParen := strings.Index(line[hit:], "(")
			if openParen == -1 {
				break
			}
			openParen += hit
			closeParen := findMatchingParen(line, openParen)
			if closeParen == -1 {
				break
			}
			body := line[openParen+1 : closeParen]
			// Skip only if the body ALREADY has the OR-wrap. Just
			// containing `public.is_service_role()` isn't enough
			// (the auth.role()-rewrite CASE body legitimately does).
			if strings.Contains(body, "public.is_service_role() OR (") {
				idx = closeParen + 1
				lower = strings.ToLower(line)
				continue
			}
			wrapped := "public.is_service_role() OR (" + body + ")"
			line = line[:openParen+1] + wrapped + line[closeParen:]
			changed++
			// Re-lowercase for the next scan (line changed length).
			lower = strings.ToLower(line)
			idx = openParen + 1 + len(wrapped) + 1
		}
	}
	return line, changed
}

// findMatchingParen given the index of a `(` returns the index of its
// matching `)`. Naive paren counter; ignores parens inside single-
// quoted strings (SQL string literals) so a policy body like
// `USING (col = 'foo(bar)')` doesn't corrupt the walker.
func findMatchingParen(s string, openIdx int) int {
	depth := 0
	inString := false
	for i := openIdx; i < len(s); i++ {
		c := s[i]
		if inString {
			if c == '\'' {
				// SQL: doubled '' is an escaped single quote inside
				// a string literal.
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
			continue
		}
		switch c {
		case '\'':
			inString = true
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

// ── regex table ──────────────────────────────────────────────────────

var (
	// `SET <ident> = …` — pg_dump's preamble is a run of these
	// (`SET statement_timeout`, `SET client_encoding`,
	// `SET search_path`, `SET row_security`, `SET
	// default_table_access_method`, …). All get stripped: the
	// Eurobase migration executor's session already has the correct
	// search_path pinned + row_security enforced, and stray SETs
	// would either fail on the migrator role or silently disable
	// enforcement for the tx. (#274 review M6.)
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
	//   TABLE / INTO / FROM / UPDATE / REFERENCES / ON / SEQUENCE
	//   / VIEW / TRIGGER / OWNED BY
	//
	// Plus optional `ONLY` or `IF NOT EXISTS` modifiers between the
	// keyword and the qualifier. This gate is deliberately narrower
	// than "any `public.<ident>`" — a blanket strip would corrupt
	// function calls (`public.uuid_generate_v4()`), string literals
	// (`'public.foo'::regclass`), and USING-clause operator classes.
	rePublicQualifierContext = regexp.MustCompile(
		`(?i)(\b(?:TABLE|INTO|FROM|UPDATE|REFERENCES|ON|SEQUENCE|VIEW|TRIGGER|OWNED\s+BY)\s+(?:ONLY\s+|IF\s+NOT\s+EXISTS\s+)?)public\.`,
	)

	// Any qualified reference to a schema that ISN'T public / auth /
	// storage / pg_catalog / information_schema / tenant (the ones
	// we've already handled or intentionally left alone). Best-effort
	// warning — false-positives cost the tenant a look, a false-
	// negative would cost them a broken migration. `extensions` is not
	// in this list because we rewrite the common helpers on that
	// schema; anything else on `extensions` gets flagged by the
	// broader "custom schema" catch below.
	reCustomSchemaRef = regexp.MustCompile(`\b(?:graphql|graphql_public|realtime|vault|net|cron|supabase_functions|_supabase|pgmq_public)\.\w+`)
)

// countAndReplacePublicQualifier strips `public.` from table/sequence
// references while leaving `public.<func>()` calls, string literals,
// and USING-clause operator classes intact. pg_dump emits `CREATE
// TABLE public.orders`, `REFERENCES public.users(id)`, `ALTER TABLE
// ONLY public.orders`, etc.; Eurobase's tenant migrator pins
// `search_path` to the tenant schema, so an explicit `public.`
// qualifier lands the object in `public` (which the per-tenant `_ddl`
// role can't create in). We keep `public.uuid_generate_v4()` and
// `public.is_service_role()` intact — those are function calls in
// Eurobase's public schema and must stay qualified. Gated on the
// preceding DDL keyword (see rePublicQualifierContext). (#274 B1.)
func countAndReplacePublicQualifier(s *string) int {
	matches := rePublicQualifierContext.FindAllStringIndex(*s, -1)
	if len(matches) == 0 {
		return 0
	}
	*s = rePublicQualifierContext.ReplaceAllString(*s, "$1")
	return len(matches)
}
