package cli

// Pass 0 of the Supabase → Eurobase translator: pg_dump objects a
// Eurobase project can't hold are commented out (kept visible in the
// file, with the reason) instead of failing the whole migration.
//
// pg_dump puts a header in front of every object:
//
//	--
//	-- Name: orders; Type: TABLE; Schema: public; Owner: -
//	--
//
// so the dump splits cleanly into one block per object. Input without
// these headers (hand-written SQL, tests) passes through unchanged.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var reLowerIdent = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)

var reDumpHeader = regexp.MustCompile(`(?m)^-- Name: (.*?); Type: (.*?); Schema: (.*?);`)

type dumpBlock struct {
	text              string
	name, typ, schema string
	hasHeader         bool
}

// splitDumpBlocks cuts a pg_dump body at each object header. The `--`
// line above a header belongs to the block it introduces.
func splitDumpBlocks(sql string) []dumpBlock {
	// A header is a line comment in code — not text inside a function
	// body or string that happens to look like one.
	commentStarts := map[int]bool{}
	segs, _ := splitCodeAndLiterals(sql)
	off := 0
	for _, seg := range segs {
		if !seg.code && strings.HasPrefix(seg.text, "--") {
			commentStarts[off] = true
		}
		off += len(seg.text)
	}
	var locs [][]int
	for _, l := range reDumpHeader.FindAllStringSubmatchIndex(sql, -1) {
		if commentStarts[l[0]] {
			locs = append(locs, l)
		}
	}
	if len(locs) == 0 {
		return []dumpBlock{{text: sql}}
	}
	starts := make([]int, len(locs))
	for i, l := range locs {
		st := l[0]
		// Include the preceding "--\n" line.
		if st >= 3 && sql[st-3:st] == "--\n" && (st == 3 || sql[st-4] == '\n') {
			st -= 3
		}
		starts[i] = st
	}
	blocks := []dumpBlock{{text: sql[:starts[0]]}}
	for i, l := range locs {
		end := len(sql)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		blocks = append(blocks, dumpBlock{
			text:      sql[starts[i]:end],
			name:      sql[l[2]:l[3]],
			typ:       sql[l[4]:l[5]],
			schema:    sql[l[6]:l[7]],
			hasHeader: true,
		})
	}
	return blocks
}

// unsupportedReason says why a block can't be imported, or "".
// skipped holds the relations already left out (materialized views and
// what depends on them).
func unsupportedReason(b dumpBlock, skipped map[string]bool) string {
	if !b.hasHeader {
		return ""
	}
	switch b.typ {
	case "EXTENSION":
		return "extensions are managed by Eurobase (the assess report lists which ones are available)"
	case "PUBLICATION", "PUBLICATION TABLE", "PUBLICATION TABLES IN SCHEMA":
		return "Supabase Realtime publication — Eurobase realtime needs no setup; events fire for writes made through the API"
	case "MATERIALIZED VIEW", "MATERIALIZED VIEW DATA":
		return "materialized views run with their owner's rights and skip RLS; Eurobase refuses them — use a view with security_invoker, or a table filled by a cron job"
	case "EVENT TRIGGER", "SERVER", "USER MAPPING", "FOREIGN DATA WRAPPER", "FOREIGN TABLE":
		return "server-level object, not available in a project"
	case "COMMENT":
		if strings.HasPrefix(b.name, "EXTENSION ") {
			return "comment on an extension"
		}
	case "SCHEMA":
		if b.name != "public" {
			return fmt.Sprintf("schema %s — Eurobase imports into one project schema", b.name)
		}
	}
	if b.schema != "public" && b.schema != "-" {
		return fmt.Sprintf("object in schema %s — only the public schema is imported", b.schema)
	}
	if refersToSkipped(b, skipped) {
		return "depends on a materialized view that isn't imported"
	}
	return ""
}

// refersToSkipped reports whether the block's code names a relation in
// skipped (an index, comment, view, trigger, policy or constraint on or
// reading it).
func refersToSkipped(b dumpBlock, skipped map[string]bool) bool {
	if len(skipped) == 0 {
		return false
	}
	segs, _ := splitCodeAndLiterals(b.text)
	var code strings.Builder
	for _, seg := range segs {
		if seg.code || strings.HasPrefix(seg.text, `"`) {
			code.WriteString(seg.text)
		} else {
			code.WriteByte(' ')
		}
	}
	text := code.String()
	for name := range skipped {
		// pg_dump writes a name unquoted only when it is a plain
		// lowercase identifier; anything else appears quoted, exactly.
		ref := `"` + regexp.QuoteMeta(name) + `"`
		if reLowerIdent.MatchString(name) {
			ref = `(?:` + ref + `|` + regexp.QuoteMeta(name) + `\b)`
		}
		re := regexp.MustCompile(`(?i:\b(?:ON|FROM|JOIN|COLUMN|VIEW|TABLE|ONLY)\s+(?:public\.)?)` + ref)
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// commentOutUnsupportedObjects turns every unsupported block into SQL
// comments and records one warning per reason.
func commentOutUnsupportedObjects(sql string, res *translationResult) string {
	blocks := splitDumpBlocks(sql)
	// Decide what to skip first, to a fixed point: a view reading a
	// skipped materialized view is skipped, and so is what reads it.
	skipped := map[string]bool{}
	reasons := make([]string, len(blocks))
	for changed := true; changed; {
		changed = false
		for i, b := range blocks {
			if reasons[i] != "" {
				continue
			}
			if r := unsupportedReason(b, skipped); r != "" {
				reasons[i] = r
				changed = true
				if b.typ == "MATERIALIZED VIEW" || b.typ == "VIEW" {
					skipped[b.name] = true
				}
			}
		}
	}
	byReason := map[string][]string{}
	var out strings.Builder
	out.Grow(len(sql) + len(sql)/16)
	for i, b := range blocks {
		reason := reasons[i]
		if reason == "" {
			out.WriteString(b.text)
			continue
		}
		byReason[reason] = append(byReason[reason], b.name)
		res.rewrites["skip:unsupported object (commented out)"]++
		switch b.typ {
		case "POLICY", "ROW SECURITY", "TRIGGER", "RULE":
			// Leaving out a restrictive policy, an ENABLE ROW LEVEL
			// SECURITY or a guard trigger opens access up.
			res.warnings = append(res.warnings, translationWarning{
				access: true,
				note:   fmt.Sprintf("%s %s is not imported (%s) — whatever it restricted is no longer restricted; recreate the check another way", strings.ToLower(b.typ), b.name, reason),
			})
		}
		out.WriteString("-- eurobase: not imported — " + reason + "\n")
		lines := strings.SplitAfter(b.text, "\n")
		for _, ln := range lines {
			if ln == "" {
				continue
			}
			if strings.HasPrefix(ln, "--") || strings.TrimSpace(ln) == "" {
				out.WriteString(ln)
				continue
			}
			out.WriteString("-- " + ln)
		}
	}
	reasonList := make([]string, 0, len(byReason))
	for r := range byReason {
		reasonList = append(reasonList, r)
	}
	sort.Strings(reasonList)
	for _, r := range reasonList {
		res.warnings = append(res.warnings, translationWarning{
			note: fmt.Sprintf("not imported (%s): %s — %s", plural(len(byReason[r]), "object"), nameSample(byReason[r]), r),
		})
	}
	return out.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func nameSample(names []string) string {
	const max = 5
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(", … (%d more)", len(names)-max)
}
