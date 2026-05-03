package query

import "strings"

// HasMultipleStatements reports whether sql contains more than one
// SQL statement separated by an unquoted semicolon. It is a heuristic
// scanner — it correctly skips over single-quoted strings, double-quoted
// identifiers, line comments, block comments, and dollar-quoted strings
// (including tagged $tag$ … $tag$ blocks). It is intended as a guard
// rail, not a full parser: false positives are acceptable (the caller
// can switch to the transaction endpoint), false negatives are not.
//
// The pgx Tx.Exec path uses the extended query protocol, which executes
// only the first statement in a multi-statement string and silently
// drops the rest. This guard rejects such input on single-statement
// endpoints with a clear error so callers cannot mistake a partial
// run for success.
func HasMultipleStatements(sql string) bool {
	s := strings.TrimSpace(sql)
	// A single trailing semicolon is fine — that's just statement
	// termination. Strip it so it doesn't trigger the guard.
	s = strings.TrimRight(s, "; \t\r\n")

	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '\'':
			// Single-quoted string. '' is an escaped quote.
			i++
			for i < n {
				if s[i] == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '"':
			// Double-quoted identifier. "" is an escaped quote.
			i++
			for i < n {
				if s[i] == '"' {
					if i+1 < n && s[i+1] == '"' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '-' && i+1 < n && s[i+1] == '-':
			// Line comment to end of line.
			i += 2
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			// Block comment, may nest in PostgreSQL.
			i += 2
			depth := 1
			for i < n && depth > 0 {
				if i+1 < n && s[i] == '/' && s[i+1] == '*' {
					depth++
					i += 2
					continue
				}
				if i+1 < n && s[i] == '*' && s[i+1] == '/' {
					depth--
					i += 2
					continue
				}
				i++
			}
		case c == '$':
			// Dollar-quoted string. Tag is [A-Za-z_][A-Za-z0-9_]* (may be empty).
			tagEnd := i + 1
			for tagEnd < n && isDollarTagChar(s[tagEnd], tagEnd == i+1) {
				tagEnd++
			}
			if tagEnd >= n || s[tagEnd] != '$' {
				// Not a dollar-quote opener — just a stray $.
				i++
				continue
			}
			tag := s[i : tagEnd+1] // includes both $s
			i = tagEnd + 1
			// Scan for the matching closer.
			closer := strings.Index(s[i:], tag)
			if closer < 0 {
				// Unterminated — treat the rest as inside the quote.
				return false
			}
			i += closer + len(tag)
		case c == ';':
			// Unquoted semicolon — anything non-whitespace after it is a
			// second statement.
			j := i + 1
			for j < n {
				cj := s[j]
				if cj == ' ' || cj == '\t' || cj == '\r' || cj == '\n' {
					j++
					continue
				}
				return true
			}
			i = j
		default:
			i++
		}
	}
	return false
}

func isDollarTagChar(c byte, first bool) bool {
	if first {
		return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
