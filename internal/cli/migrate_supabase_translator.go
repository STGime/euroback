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
// It processes the input line-by-line (comments handled per line) and
// applies regex-based rewrites. Kept regex-based on purpose: a full
// SQL parser would be an order of magnitude more code and would still
// miss the same edge cases that generate the warnings list. Six
// rewrites cover:
//
//  1. Strip `SET search_path = …` — Eurobase's executor pins this per
//     tenant. Leaving it in would set the wrong search_path.
//  2. Strip `CREATE SCHEMA public;` — schema is already provisioned.
//  3. Rewrite `REFERENCES auth.users(id)` → `REFERENCES users(id)`.
//  4. Rewrite `auth.uid()` → `auth_uid()` (Eurobase's per-tenant helper).
//  5. Rewrite `auth.role()` → a CASE that preserves the "service_role
//     vs authenticated" distinction using Eurobase's helpers.
//  6. Rewrite `storage.objects` → `storage_objects`.
//  7. Wrap every policy USING(...) / WITH CHECK(...) in
//     `public.is_service_role() OR (...)` so service-key writes keep
//     working after the move.
//
// Warns (not rewrites) — the tenant must review:
//   - Any use of `auth.jwt() ->> …` on a project-specific claim.
//   - `auth.email()` — Eurobase's `auth_email()` exists but the
//     policy body is worth eyeballing.
//   - Any non-public / non-storage schema qualifier (extensions in
//     custom schemas, foreign-data wrappers, etc.).
func Translate(input string) translationResult {
	res := translationResult{rewrites: map[string]int{}}

	var out strings.Builder
	out.Grow(len(input))
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		lineNum := i + 1
		rewritten, changed, warns := translateLine(line, lineNum)
		for name := range changed {
			res.rewrites[name] += changed[name]
		}
		res.warnings = append(res.warnings, warns...)
		if rewritten == "" && strings.TrimSpace(line) == "" {
			out.WriteString("\n")
			continue
		}
		if rewritten == skipLineSentinel {
			continue
		}
		out.WriteString(rewritten)
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}
	res.sql = out.String()
	return res
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
	if reSetSearchPath.MatchString(trimmed) {
		fired["strip:SET search_path"]++
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

	// Policy-body wrapping — done AFTER the atom rewrites so the
	// wrapped body already has Eurobase-shape function calls.
	if reCreatePolicy.MatchString(out) {
		if wrapped, n := wrapPolicyBody(out); n > 0 {
			out = wrapped
			fired["rewrite:policy body OR'd with is_service_role()"] += n
		}
	}

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
	reSetSearchPath      = regexp.MustCompile(`(?i)^\s*SET\s+search_path\s*=`)
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

	// `CREATE POLICY` — used to gate the wrapPolicyBody call.
	reCreatePolicy = regexp.MustCompile(`(?i)\bCREATE\s+POLICY\b`)

	// Any qualified reference to a schema that ISN'T public / auth /
	// storage / pg_catalog / information_schema / tenant (the ones
	// we've already handled or intentionally left alone). Best-effort
	// warning — this regex will false-positive on things like
	// `extensions.uuid_generate_v4()` which we've already migrated in
	// spirit. That's fine; false-positives cost the tenant a look, a
	// false-negative would cost them a broken migration.
	reCustomSchemaRef = regexp.MustCompile(`\b(?:extensions|graphql|graphql_public|realtime|vault|net|cron|supabase_functions|_supabase|pgmq_public)\.\w+`)
)
