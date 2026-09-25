package query

// HasMultipleStatements reports whether sql contains more than one
// SQL statement separated by an unquoted semicolon. It is a heuristic
// scanner — it correctly skips over single-quoted strings, double-quoted
// identifiers, line comments, block comments, and dollar-quoted strings
// (including tagged $tag$ … $tag$ blocks). It is intended as a guard
// rail, not a full parser: false positives are acceptable (the caller
// can switch to the transaction endpoint), false negatives are not.
//
// Note: pgx's Exec only uses the extended protocol when there are
// arguments; with none it sends the simple protocol, which runs every
// statement. Callers that need the server to enforce a single statement
// use PgConn.ExecParams (the engine's execCustomerStatement, cron's
// execExtended). This guard rejects such input on single-statement
// endpoints with a clear error so callers cannot mistake a partial
// run for success.
func HasMultipleStatements(sql string) bool {
	// Uses the shared tokenizer (cross_schema.go), which knows every
	// PostgreSQL quoting form — including E'…' escape strings and U&
	// identifiers — so this check and the other guards read the input the
	// same way. Every statement starts with a keyword, so "a statement
	// follows" == "a token follows an unquoted ';'".
	seenSemi := false
	for _, t := range scanIdentifiersAndDots(sql) {
		if t.kind == tokSemi {
			seenSemi = true
			continue
		}
		if seenSemi {
			return true
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
