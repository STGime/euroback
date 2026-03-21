package query

import (
	"fmt"
	"regexp"
	"strings"
)

// blockedKeywords are SQL keywords that indicate non-SELECT statements.
var blockedKeywords = []string{
	"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE",
	"TRUNCATE", "GRANT", "REVOKE", "SET", "COPY", "EXECUTE",
	"CALL", "LOCK", "VACUUM", "REINDEX", "CLUSTER", "COMMENT",
}

// lineCommentRe matches single-line SQL comments (-- ...).
var lineCommentRe = regexp.MustCompile(`--[^\n]*`)

// blockCommentRe matches block SQL comments (/* ... */).
var blockCommentRe = regexp.MustCompile(`/\*[\s\S]*?\*/`)

// ValidateSelectOnly checks that the given SQL string is a read-only SELECT
// (or WITH/CTE) query. It returns an error if the query contains DML, DDL,
// semicolons (statement chaining), or other unsafe patterns.
func ValidateSelectOnly(sql string) error {
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("query cannot be empty")
	}

	// Strip comments to prevent hiding keywords inside them.
	stripped := lineCommentRe.ReplaceAllString(sql, " ")
	stripped = blockCommentRe.ReplaceAllString(stripped, " ")

	// Allow a single trailing semicolon (natural habit), but block
	// multiple semicolons which indicate statement chaining.
	stripped = strings.TrimSpace(stripped)
	if strings.HasSuffix(stripped, ";") {
		stripped = strings.TrimSpace(stripped[:len(stripped)-1])
	}
	if strings.Contains(stripped, ";") {
		return fmt.Errorf("only single statements are allowed")
	}

	// Normalize whitespace for keyword detection.
	normalized := strings.Join(strings.Fields(stripped), " ")
	upper := strings.ToUpper(normalized)

	// First keyword must be SELECT or WITH.
	firstWord := strings.SplitN(strings.TrimSpace(upper), " ", 2)[0]
	if firstWord != "SELECT" && firstWord != "WITH" {
		return fmt.Errorf("only SELECT queries are allowed")
	}

	// Block dangerous keywords anywhere in the query.
	for _, kw := range blockedKeywords {
		// Match as whole word to avoid false positives (e.g., "updated_at").
		pattern := `\b` + kw + `\b`
		matched, _ := regexp.MatchString(pattern, upper)
		if matched {
			return fmt.Errorf("only SELECT queries are allowed")
		}
	}

	// Block SELECT INTO (creates a new table).
	if matched, _ := regexp.MatchString(`\bSELECT\b.*\bINTO\b`, upper); matched {
		return fmt.Errorf("SELECT INTO is not allowed")
	}

	// Block FOR UPDATE / FOR SHARE (row-level locking).
	if matched, _ := regexp.MatchString(`\bFOR\s+(UPDATE|SHARE|NO\s+KEY\s+UPDATE|KEY\s+SHARE)\b`, upper); matched {
		return fmt.Errorf("FOR UPDATE/SHARE is not allowed")
	}

	return nil
}
