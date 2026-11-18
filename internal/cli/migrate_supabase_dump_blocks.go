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

var reDumpHeader = regexp.MustCompile(`(?m)^-- Name: (.*?); Type: (.*?); Schema: (.*?);`)

type dumpBlock struct {
	text              string
	name, typ, schema string
	hasHeader         bool
}

// splitDumpBlocks cuts a pg_dump body at each object header. The `--`
// line above a header belongs to the block it introduces.
func splitDumpBlocks(sql string) []dumpBlock {
	locs := reDumpHeader.FindAllStringSubmatchIndex(sql, -1)
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
func unsupportedReason(b dumpBlock, matviews map[string]bool) string {
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
		if strings.HasPrefix(b.name, "MATERIALIZED VIEW ") && matviews[strings.TrimPrefix(b.name, "MATERIALIZED VIEW ")] {
			return "belongs to a materialized view that isn't imported"
		}
	case "SCHEMA":
		if b.name != "public" {
			return fmt.Sprintf("schema %s — Eurobase imports into one project schema", b.name)
		}
	case "INDEX":
		for mv := range matviews {
			if regexp.MustCompile(`(?i)\bON\s+(ONLY\s+)?(public\.)?"?` + regexp.QuoteMeta(mv) + `"?\s`).MatchString(b.text) {
				return "index on a materialized view that isn't imported"
			}
		}
	}
	if b.schema != "public" && b.schema != "-" {
		return fmt.Sprintf("object in schema %s — only the public schema is imported", b.schema)
	}
	return ""
}

// commentOutUnsupportedObjects turns every unsupported block into SQL
// comments and records one warning per reason.
func commentOutUnsupportedObjects(sql string, res *translationResult) string {
	blocks := splitDumpBlocks(sql)
	matviews := map[string]bool{}
	for _, b := range blocks {
		if b.typ == "MATERIALIZED VIEW" {
			matviews[b.name] = true
		}
	}
	byReason := map[string][]string{}
	var out strings.Builder
	out.Grow(len(sql) + len(sql)/16)
	for _, b := range blocks {
		reason := unsupportedReason(b, matviews)
		if reason == "" {
			out.WriteString(b.text)
			continue
		}
		byReason[reason] = append(byReason[reason], b.name)
		res.rewrites["skip:unsupported object (commented out)"]++
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
	reasons := make([]string, 0, len(byReason))
	for r := range byReason {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for _, r := range reasons {
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
