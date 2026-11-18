package cli

// Statement-level pass of the Supabase → Eurobase translator (Pass 2).
//
// Splits the Pass-1 output into statements (a `;` in code ends one;
// `;` inside literals, dollar-quoted bodies and comments doesn't) and
// rewrites by statement kind:
//
//   - CREATE POLICY: the `TO <roles>` clause becomes a condition
//     (Eurobase has no anon / authenticated roles — every SDK request
//     runs as the same database role and the end user is identified by
//     auth_uid()), then every USING / WITH CHECK body is wrapped in
//     `public.is_service_role() OR (...)` so service-key requests keep
//     the access Supabase's service_role (BYPASSRLS) had.
//   - CREATE FUNCTION / PROCEDURE: SECURITY DEFINER is removed (Eurobase
//     refuses functions that run with their owner's rights; the function
//     now runs with the caller's rights and is reported), and the
//     dollar-quoted body gets the same rewrites as top-level code
//     (auth.uid() → auth_uid(), public.<name> → <name>, …), so it works
//     at runtime in a project schema.
//   - CREATE VIEW: forced to security_invoker (Eurobase refuses views
//     that run with their owner's rights).
//
// A statement's head is read from its code only: pg_dump puts a
// `-- Name: …; Type: …` comment block in front of every object, so a
// raw "starts with CREATE POLICY" check never matches real dumps.

import (
	"fmt"
	"regexp"
	"strings"
)

// translateStatements runs the statement-level rewrites over sql.
func translateStatements(sql string, res *translationResult) string {
	segments, _ := splitCodeAndLiterals(sql)
	var out strings.Builder
	out.Grow(len(sql) + len(sql)/8)
	var stmt strings.Builder
	flush := func() {
		if stmt.Len() == 0 {
			return
		}
		out.WriteString(translateStatement(stmt.String(), res))
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
	return out.String()
}

var (
	reHeadPolicy   = regexp.MustCompile(`(?i)^CREATE\s+POLICY\b`)
	reHeadFunction = regexp.MustCompile(`(?i)^CREATE\s+(OR\s+REPLACE\s+)?(FUNCTION|PROCEDURE)\s+([^\s(]+)`)
	reHeadTrigger  = regexp.MustCompile(`(?i)^CREATE\s+(OR\s+REPLACE\s+)?(?:CONSTRAINT\s+)?TRIGGER\s+("(?:[^"]|"")*"|\S+)`)
	reHeadTable    = regexp.MustCompile(`(?i)^CREATE\s+(?:UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?("(?:[^"]|"")*"|[^\s(]+)`)
	reHeadView     = regexp.MustCompile(`(?i)^CREATE\s+(OR\s+REPLACE\s+)?(TEMP\s+|TEMPORARY\s+|RECURSIVE\s+)*VIEW\s+([^\s(]+)`)
)

func translateStatement(s string, res *translationResult) string {
	head := statementCodeHead(s)
	switch {
	case reHeadPolicy.MatchString(head):
		out := translatePolicy(s, res)
		flagDefinerUse(out, "policy "+policyName(out), res)
		return out
	case reHeadTrigger.MatchString(head):
		flagDefinerUse(s, "trigger "+reHeadTrigger.FindStringSubmatch(head)[2], res)
		return s
	case reHeadTable.MatchString(head):
		name := normalizeName(reHeadTable.FindStringSubmatch(head)[1])
		if platformTableNames[name] {
			res.warnings = append(res.warnings, translationWarning{
				note: fmt.Sprintf("table %s has the name of a table every Eurobase project already has — rename it in Supabase (and in your app) before importing, or the migration fails", name),
			})
		}
		return s
	case reHeadFunction.MatchString(head):
		name := reHeadFunction.FindStringSubmatch(head)[3]
		return translateFunction(s, name, res)
	case reHeadView.MatchString(head):
		name := reHeadView.FindStringSubmatch(head)[3]
		return translateView(s, name, res)
	}
	return s
}

// statementCodeHead returns the statement's code with comments and
// literals removed (literals replaced by a space), leading whitespace
// trimmed, cut to the first 200 bytes — enough to read its kind and
// name. Double-quoted identifiers are kept (a function or view name may
// be quoted).
func statementCodeHead(s string) string {
	segs, _ := splitCodeAndLiterals(s)
	var b strings.Builder
	for _, seg := range segs {
		switch {
		case seg.code:
			b.WriteString(seg.text)
		case strings.HasPrefix(seg.text, `"`):
			b.WriteString(seg.text)
		default:
			b.WriteByte(' ')
		}
		if b.Len() > 400 {
			break
		}
	}
	h := strings.TrimLeft(b.String(), " \t\r\n")
	if len(h) > 200 {
		h = h[:200]
	}
	return h
}

// codeMatches returns the [start,end) offsets of re's matches in s that
// start in code (not inside a literal, comment or quoted identifier).
func codeMatches(s string, re *regexp.Regexp) [][]int {
	mask, _ := buildCodeMask(s)
	var out [][]int
	for _, m := range re.FindAllStringIndex(s, -1) {
		if m[0] < len(mask) && mask[m[0]] {
			out = append(out, m)
		}
	}
	return out
}

// ── policies ─────────────────────────────────────────────────────────

var (
	rePolicyClause = regexp.MustCompile(`(?i)\b(USING|WITH\s+CHECK)\s*\(`)
	rePolicyTo     = regexp.MustCompile(`(?i)\bTO\b`)
	reRestrictive  = regexp.MustCompile(`(?i)\bAS\s+RESTRICTIVE\b`)
)

func translatePolicy(s string, res *translationResult) string {
	s = rewritePolicyRoles(s, res)
	out, n := mapPolicyBodies(s, func(body string) string {
		// Already translated (a rerun on translated input): the
		// clause's code starts with the wrap. Checked per clause, on
		// the body text itself — a literal elsewhere can't suppress it.
		if strings.HasPrefix(strings.TrimSpace(body), "public.is_service_role() OR (") {
			return body
		}
		return "public.is_service_role() OR (" + body + ")"
	})
	if n > 0 {
		res.rewrites["rewrite:policy body OR'd with is_service_role()"] += n
	}
	return out
}

// mapPolicyBodies replaces the body X of every `USING (X)` and
// `WITH CHECK (X)` clause that starts in code with f(X). Clauses nested
// inside a body (a subquery's `JOIN … USING (col)`) are skipped because
// the scan resumes after each body.
func mapPolicyBodies(s string, f func(string) string) (string, int) {
	changed := 0
	pos := 0
	for {
		var hit []int
		for _, m := range codeMatches(s, rePolicyClause) {
			if m[0] >= pos {
				hit = m
				break
			}
		}
		if hit == nil {
			return s, changed
		}
		open := hit[1] - 1 // the '(' the regex ended on
		closeIdx := findMatchingParen(s, open)
		if closeIdx == -1 {
			return s, changed
		}
		body := f(s[open+1 : closeIdx])
		s = s[:open+1] + body + s[closeIdx:]
		changed++
		pos = open + 1 + len(body) + 1
	}
}

// rewritePolicyRoles turns `TO <roles>` into a condition ANDed into the
// policy's bodies. Supabase roles map to who is calling on Eurobase:
//
//	anon          → auth_uid() IS NULL      (no signed-in end user)
//	authenticated → auth_uid() IS NOT NULL  (a signed-in end user)
//	service_role  → nobody but the service key (added by the wrap)
//	public, or anon together with authenticated → everyone (no condition)
//
// An unknown role is left in place (the apply then fails naming it) and
// reported.
func rewritePolicyRoles(s string, res *translationResult) string {
	clauses := codeMatches(s, rePolicyClause)
	headEnd := len(s)
	if len(clauses) > 0 {
		headEnd = clauses[0][0]
	} else if i := strings.LastIndexByte(s, ';'); i >= 0 {
		headEnd = i
	}
	var to []int
	for _, m := range codeMatches(s, rePolicyTo) {
		if m[0] < headEnd {
			to = m
		}
	}
	if to == nil {
		return s
	}
	roleText := s[to[1]:headEnd]
	roles := map[string]bool{}
	for _, r := range strings.Split(roleText, ",") {
		r = strings.ToLower(strings.Trim(strings.TrimSpace(r), `"`))
		if r != "" {
			roles[r] = true
		}
	}
	anon, authd, svc := roles["anon"], roles["authenticated"], roles["service_role"]
	for r := range roles {
		if r != "anon" && r != "authenticated" && r != "service_role" && r != "public" {
			res.warnings = append(res.warnings, translationWarning{
				note: fmt.Sprintf("policy %s is for role %q, which doesn't exist on Eurobase — rewrite it with auth_uid() / public.is_service_role() conditions", policyName(s), r),
			})
			return s
		}
	}
	var cond string
	switch {
	case roles["public"], anon && authd:
		cond = "" // everyone
	case anon:
		cond = "auth_uid() IS NULL"
	case authd:
		cond = "auth_uid() IS NOT NULL"
	case svc:
		cond = "false" // the service-key wrap admits service calls
	}
	restrictive := reRestrictive.MatchString(statementCodeHead(s[:to[0]]))
	s = s[:to[0]] + s[headEnd:]
	res.rewrites["rewrite:policy TO <role> → condition"]++
	if cond == "" {
		return s
	}
	out, _ := mapPolicyBodies(s, func(body string) string {
		if restrictive {
			// A restrictive policy only restricts the roles it names;
			// everyone else passes it.
			return "(NOT (" + cond + ")) OR (" + body + ")"
		}
		return "(" + cond + ") AND (" + body + ")"
	})
	return out
}

var rePolicyName = regexp.MustCompile(`(?is)^CREATE\s+POLICY\s+("(?:[^"]|"")*"|\S+)`)

func policyName(s string) string {
	if m := rePolicyName.FindStringSubmatch(statementCodeHead(s)); m != nil {
		return m[1]
	}
	return "(unnamed)"
}

// ── functions ────────────────────────────────────────────────────────

var (
	reSecurityDefiner = regexp.MustCompile(`(?i)[ \t]*\bSECURITY\s+DEFINER\b[ \t]*`)
	reEndsWithAS      = regexp.MustCompile(`(?i)\bAS\s*$`)
)

func translateFunction(s, name string, res *translationResult) string {
	// SECURITY DEFINER sits outside the body, in code.
	if ms := codeMatches(s, reSecurityDefiner); len(ms) > 0 {
		// A space, not nothing: pg_dump writes
		// `LANGUAGE sql STABLE SECURITY DEFINER PARALLEL SAFE`.
		for i := len(ms) - 1; i >= 0; i-- {
			s = s[:ms[i][0]] + " " + s[ms[i][1]:]
		}
		res.rewrites["rewrite:SECURITY DEFINER removed"]++
		res.definers[normalizeName(name)] = true
		res.warnings = append(res.warnings, translationWarning{
			note: fmt.Sprintf("function %s was SECURITY DEFINER; it now runs with the caller's rights, so it only sees rows the caller's RLS allows. Eurobase doesn't run functions with their owner's rights — if it did work the caller can't (e.g. a trigger writing to another table), move that work to an edge function or a webhook (see the assess report)", name),
		})
		s = "\n-- eurobase: " + name + " was SECURITY DEFINER in Supabase; it now runs with the caller's rights.\n" + strings.TrimLeft(s, "\n")
	}

	// Translate the body: the dollar-quoted segment right after AS. A
	// dollar-quoted parameter default is data and stays as it is.
	segs, _ := splitCodeAndLiterals(s)
	var b strings.Builder
	b.Grow(len(s))
	var code strings.Builder // code so far, to see what precedes a segment
	for _, seg := range segs {
		if seg.code {
			code.WriteString(seg.text)
			b.WriteString(seg.text)
			continue
		}
		if !strings.HasPrefix(seg.text, "$") || !reEndsWithAS.MatchString(code.String()) {
			if !strings.HasPrefix(seg.text, "--") && !strings.HasPrefix(seg.text, "/*") {
				code.WriteString(" _ ") // a literal: no longer right after AS
			}
			b.WriteString(seg.text)
			continue
		}
		code.WriteString(" _ ")
		tag, tagLen, ok := parseDollarTag(seg.text, 0)
		if !ok || len(seg.text) < 2*tagLen || !strings.HasSuffix(seg.text, tag) {
			b.WriteString(seg.text)
			continue
		}
		inner := seg.text[tagLen : len(seg.text)-tagLen]
		mask, _ := buildCodeMask(inner)
		rewritten, fired, warns := translateCodeSpans(inner, mask, 0)
		for k, v := range fired {
			res.rewrites[k+" (function bodies)"] += v
		}
		for _, w := range warns {
			w.note = "in function " + name + ": " + w.note
			res.warnings = append(res.warnings, w)
		}
		b.WriteString(tag + rewritten + tag)
	}
	return b.String()
}

// platformTableNames are the tables every project schema has.
var platformTableNames = map[string]bool{
	"users": true, "user_identities": true, "refresh_tokens": true, "email_tokens": true,
	"storage_objects": true, "storage_shared_prefixes": true, "vault_secrets": true,
}

// normalizeName drops a schema qualifier and folds an unquoted name to
// lower case, as Postgres does.
func normalizeName(n string) string {
	// Last dot outside double quotes separates the schema.
	last, inQuote := 0, false
	for i := 0; i < len(n); i++ {
		switch n[i] {
		case '"':
			inQuote = !inQuote
		case '.':
			if !inQuote {
				last = i + 1
			}
		}
	}
	n = n[last:]
	if len(n) >= 2 && n[0] == '"' && n[len(n)-1] == '"' {
		return strings.ReplaceAll(n[1:len(n)-1], `""`, `"`)
	}
	return strings.ToLower(n)
}

// flagDefinerUse reports a policy or trigger that calls a function
// which was SECURITY DEFINER. Running with the caller's rights, such a
// function sees only the rows the caller may see — a check for the
// absence of rows (`NOT is_banned()`, `NOT EXISTS (…)`) then passes
// for callers it used to stop.
func flagDefinerUse(stmt, what string, res *translationResult) {
	if len(res.definers) == 0 {
		return
	}
	code := strings.ToLower(statementCode(stmt))
	for fn := range res.definers {
		if regexp.MustCompile(`(^|[^a-z0-9_$."])("?` + regexp.QuoteMeta(strings.ToLower(fn)) + `"?)\s*\(`).MatchString(code) {
			res.warnings = append(res.warnings, translationWarning{
				access: true,
				note:   fmt.Sprintf("%s calls %s, which was SECURITY DEFINER and now runs with the caller's rights: it only sees rows the caller may see, so a check for the absence of rows (e.g. NOT is_banned(), NOT EXISTS …) can now let callers through. Rewrite the check so the caller's own policies allow what it reads, or move it out of the database", what, fn),
			})
		}
	}
}

// statementCode is the statement's code with literals and comments
// blanked (quoted identifiers kept).
func statementCode(s string) string {
	segs, _ := splitCodeAndLiterals(s)
	var b strings.Builder
	for _, seg := range segs {
		if seg.code || strings.HasPrefix(seg.text, `"`) {
			b.WriteString(seg.text)
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// ── views ────────────────────────────────────────────────────────────

var (
	reViewSecurityInvoker = regexp.MustCompile(`(?i)\bsecurity_invoker\s*=\s*'?(true|on|1|yes)'?`)
	reViewSecurityOff     = regexp.MustCompile(`(?i)\bsecurity_invoker\s*=\s*'?(false|off|0|no)'?`)
	reViewWith            = regexp.MustCompile(`(?i)\bWITH\s*\(`)
	reViewAs              = regexp.MustCompile(`(?i)\bAS\b`)
)

func translateView(s, name string, res *translationResult) string {
	// Work on the part before the view's AS: the options live there,
	// and the query after AS may contain anything.
	as := codeMatches(s, reViewAs)
	if len(as) == 0 {
		return s
	}
	headEnd := as[0][0]
	head := s[:headEnd]
	if reViewSecurityInvoker.MatchString(head) {
		return s
	}
	switch {
	case reViewSecurityOff.MatchString(head):
		head = reViewSecurityOff.ReplaceAllString(head, "security_invoker='true'")
	case reViewWith.MatchString(head):
		loc := reViewWith.FindStringIndex(head)
		head = head[:loc[1]] + "security_invoker='true', " + head[loc[1]:]
	default:
		head = strings.TrimRight(head, " \t\n") + " WITH (security_invoker='true') "
	}
	res.rewrites["rewrite:view → security_invoker"]++
	res.warnings = append(res.warnings, translationWarning{
		note: fmt.Sprintf("view %s now applies the caller's RLS (security_invoker); in Supabase it ran with its owner's rights. Callers only see rows the base tables' policies allow", name),
	})
	return head + s[headEnd:]
}
