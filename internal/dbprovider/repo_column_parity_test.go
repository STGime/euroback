package dbprovider

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestRepoQueryScanParity catches the SELECT / Scan column-count
// mismatch class of bug (last hit in PR-B — deprovision sweep + backfill
// sweep both crashed the moment they saw a row, because the readonly_*
// columns were added to SELECT but not to Scan). Build + unit tests
// never caught it — the destinations only get counted at Scan-time
// against a live driver.
//
// This test parses repo.go itself: for every `const q = \`...\`` SELECT
// block, count the comma-separated column expressions in the SELECT
// list; for every `.Scan(` invocation that follows, count the argument
// destinations. Both counts must match per function.
//
// String-parsing goes a long way here because our repo consistently
// wraps SELECT in a raw string with a `SELECT ... FROM` shape and
// hand-formats destinations one-per-comma. If future refactors adopt
// a different shape (query builders, generated helpers), this test
// stops applying to those functions — it's a floor, not a ceiling.
//
// If the count mismatch is because a column was added to SELECT but
// Scan didn't, or vice versa, the failure message points at the
// offending function and shows both counts.
func TestRepoQueryScanParity(t *testing.T) {
	src, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatalf("read repo.go: %v", err)
	}
	text := string(src)

	// Split by function so each mismatch names the offending function
	// (a single-file scan would just say "somewhere in repo.go").
	funcRE := regexp.MustCompile(`(?m)^func \(r \*Repo\) (\w+)\(`)
	locs := funcRE.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		t.Fatal("repo.go: no Repo methods found — parser broken")
	}

	for i, loc := range locs {
		name := text[loc[2]:loc[3]]
		start := loc[0]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := text[start:end]

		selectCounts := extractSelectColumnCounts(body)
		scanCounts := extractScanDestinationCounts(body)

		// A function may have zero SELECTs (UPDATE-only helpers like
		// SetRuntimeCredentials / MarkDeleted / HardDelete) — skip.
		if len(selectCounts) == 0 && len(scanCounts) == 0 {
			continue
		}

		// Pairing rule: for each SELECT block we expect one Scan (or
		// one QueryRow + Scan chain). If the counts diverge, that's a
		// structural change this test can't reason about — surface it
		// so the author decides whether to update the test or the code.
		if len(selectCounts) != len(scanCounts) {
			t.Errorf("%s: SELECT count %d != Scan count %d — structure changed, this test needs an update or the function has a mismatch",
				name, len(selectCounts), len(scanCounts))
			continue
		}

		for j := range selectCounts {
			if selectCounts[j] != scanCounts[j] {
				t.Errorf("%s: SELECT block %d has %d columns but Scan has %d destinations — column added on one side but not the other (this is the #344 / PR-B bug class)",
					name, j, selectCounts[j], scanCounts[j])
			}
		}
	}
}

// extractSelectColumnCounts finds every `const q = \`...\`` block in the
// given function body that contains a SELECT and returns the number of
// comma-separated columns in each SELECT list.
func extractSelectColumnCounts(body string) []int {
	// Match a raw-string const query. Non-greedy so we get one per
	// block, not one that spans multiple queries in the same function.
	blockRE := regexp.MustCompile("(?s)const q = `(.*?)`")
	blocks := blockRE.FindAllStringSubmatch(body, -1)
	counts := make([]int, 0, len(blocks))
	for _, b := range blocks {
		sql := b[1]
		// Only count SELECT queries — UPDATE / DELETE / INSERT without
		// RETURNING have no scannable columns. INSERT with RETURNING
		// (InsertProvisioning) has a RETURNING clause that behaves the
		// same as a SELECT list, so treat "RETURNING" as an alias.
		//
		// The column-list boundary is:
		//   SELECT   ...   FROM
		//   RETURNING ...  (end of string, or trailing whitespace)
		//
		// We accept whichever the query uses.
		if list, ok := extractColumnList(sql); ok {
			counts = append(counts, countColumns(list))
		}
	}
	return counts
}

// extractColumnList returns the text between SELECT/RETURNING and the
// next FROM / end-of-query. Returns ("", false) if neither keyword is
// present (i.e. this is a bare UPDATE / DELETE without RETURNING).
func extractColumnList(sql string) (string, bool) {
	upper := strings.ToUpper(sql)
	var start int
	if i := strings.Index(upper, "SELECT "); i >= 0 {
		start = i + len("SELECT ")
	} else if i := strings.Index(upper, "RETURNING "); i >= 0 {
		start = i + len("RETURNING ")
	} else {
		return "", false
	}
	rest := sql[start:]
	upperRest := strings.ToUpper(rest)
	// FROM ends a SELECT-list; RETURNING lists run to end-of-string.
	if i := strings.Index(upperRest, " FROM "); i >= 0 {
		return rest[:i], true
	}
	if i := strings.Index(upperRest, "\nFROM "); i >= 0 {
		return rest[:i], true
	}
	return rest, true
}

// countColumns counts comma-separated expressions in a SELECT list,
// ignoring commas inside parenthesised expressions (function calls,
// casts, coalesce, etc.). Whitespace-only entries are not counted.
func countColumns(list string) int {
	depth := 0
	count := 0
	current := strings.Builder{}
	flush := func() {
		if strings.TrimSpace(current.String()) != "" {
			count++
		}
		current.Reset()
	}
	for _, r := range list {
		switch r {
		case '(':
			depth++
			current.WriteRune(r)
		case ')':
			depth--
			current.WriteRune(r)
		case ',':
			if depth == 0 {
				flush()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return count
}

// extractScanDestinationCounts finds every `.Scan(` invocation in the
// function body (`Scan(&x, &y, …)` or `rows.Scan(&x, &y, …)`) and
// returns the number of destinations in each.
func extractScanDestinationCounts(body string) []int {
	counts := []int{}
	i := 0
	for i < len(body) {
		idx := strings.Index(body[i:], ".Scan(")
		if idx < 0 {
			break
		}
		start := i + idx + len(".Scan(")
		// Find the matching closing paren.
		depth := 1
		j := start
		for ; j < len(body); j++ {
			switch body[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			break
		}
		args := body[start:j]
		counts = append(counts, countColumns(args))
		i = j + 1
	}
	return counts
}
