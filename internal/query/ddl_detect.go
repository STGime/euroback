package query

import (
	"regexp"
	"strings"
)

// DetectedDDL describes a DDL operation that a SQL-runner caller
// issued. Used by the SQL handlers (HandleSQL / HandlePlatformSQL /
// HandlePlatformSQLTransaction) to feed the migration-history audit
// for any DDL that lands outside the typed handlers — i.e. console
// SQL editor, MCP runSQL, SDK runSQL. Closes #120.
//
// Detail mirrors the shape `logSchemaChange` writes: a small JSON
// object that the migration-history UI can render. Source = "sql"
// flags these as coming from the SQL-runner path so they can be
// distinguished from typed-handler audits when needed.
type DetectedDDL struct {
	Action     string
	TableName  string
	ColumnName *string
	Detail     map[string]any
}

// DetectDDL scans a single SQL statement and returns the migration-
// history-shaped audit rows it implies. Returns an empty slice for
// statements that don't include any recognised DDL (SELECT / INSERT
// / UPDATE / DELETE / GRANT / CREATE FUNCTION / CREATE POLICY / etc.).
//
// String and dollar-quoted literals, line comments, and block
// comments are stripped before matching so a SELECT against a
// table called "create_table" or a CREATE FUNCTION body that
// happens to contain the text "DROP TABLE" doesn't trigger a
// false positive.
//
// The detector is intentionally lenient — when in doubt it returns
// nothing rather than mislabel an event. Direct DB access (psql
// shell, etc.) still bypasses this audit; that's the accepted
// limitation of the option-B approach (see issue #120).
func DetectDDL(sql string) []DetectedDDL {
	clean := stripStringsAndCommentsForDetect(sql)
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return nil
	}

	var ops []DetectedDDL

	for _, re := range detectors {
		matches := re.pattern.FindAllStringSubmatch(clean, -1)
		for _, m := range matches {
			op := re.build(m)
			if op != nil {
				ops = append(ops, *op)
			}
		}
	}

	return ops
}

// detector pairs a precompiled regex with a function that builds the
// audit row from the regex's capture groups. Keeping the two
// together makes adding a new DDL shape a single-block change.
type detector struct {
	pattern *regexp.Regexp
	build   func([]string) *DetectedDDL
}

// ident matches a bare or double-quoted SQL identifier.
const ident = `(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)`

// optSchema matches an optional `schema.` prefix.
const optSchema = `(?:` + ident + `\s*\.\s*)?`

func unquoteIdent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func strPtr(s string) *string { return &s }

var detectors = []detector{
	{
		// CREATE TABLE [IF NOT EXISTS] [schema.]name (...)
		pattern: regexp.MustCompile(`(?is)\bCREATE\s+(?:GLOBAL\s+|LOCAL\s+|TEMP(?:ORARY)?\s+|UNLOGGED\s+)*TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + optSchema + `(` + ident + `)`),
		build: func(m []string) *DetectedDDL {
			return &DetectedDDL{
				Action:    "create_table",
				TableName: unquoteIdent(m[1]),
				Detail:    map[string]any{"source": "sql"},
			}
		},
	},
	{
		// DROP TABLE [IF EXISTS] [schema.]name [, [schema.]name ...] [CASCADE|RESTRICT]
		// We only match the first table name; multi-target DROP is rare in
		// practice and the second pass below picks up additional entries.
		pattern: regexp.MustCompile(`(?is)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?` + optSchema + `(` + ident + `)`),
		build: func(m []string) *DetectedDDL {
			return &DetectedDDL{
				Action:    "drop_table",
				TableName: unquoteIdent(m[1]),
				Detail:    map[string]any{"source": "sql"},
			}
		},
	},
	{
		// ALTER TABLE [IF EXISTS] [ONLY] [schema.]name ADD COLUMN [IF NOT EXISTS] name type
		pattern: regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` + optSchema + `(` + ident + `)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + ident + `)\s+([A-Za-z][A-Za-z0-9_ ]*)`),
		build: func(m []string) *DetectedDDL {
			col := unquoteIdent(m[2])
			return &DetectedDDL{
				Action:     "add_column",
				TableName:  unquoteIdent(m[1]),
				ColumnName: strPtr(col),
				Detail: map[string]any{
					"source": "sql",
					"type":   strings.TrimSpace(m[3]),
				},
			}
		},
	},
	{
		// ALTER TABLE [...] DROP COLUMN [IF EXISTS] name
		pattern: regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` + optSchema + `(` + ident + `)\s+DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?(` + ident + `)`),
		build: func(m []string) *DetectedDDL {
			col := unquoteIdent(m[2])
			return &DetectedDDL{
				Action:     "drop_column",
				TableName:  unquoteIdent(m[1]),
				ColumnName: strPtr(col),
				Detail:     map[string]any{"source": "sql"},
			}
		},
	},
	{
		// ALTER TABLE [...] RENAME [COLUMN] old TO new
		// Distinguishes RENAME-table (no column) from RENAME-column;
		// the table form is handled by the next detector.
		pattern: regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` + optSchema + `(` + ident + `)\s+RENAME\s+COLUMN\s+(` + ident + `)\s+TO\s+(` + ident + `)`),
		build: func(m []string) *DetectedDDL {
			oldCol := unquoteIdent(m[2])
			newCol := unquoteIdent(m[3])
			return &DetectedDDL{
				Action:     "alter_column",
				TableName:  unquoteIdent(m[1]),
				ColumnName: strPtr(oldCol),
				Detail: map[string]any{
					"source":     "sql",
					"rename_to":  newCol,
					"kind":       "rename_column",
				},
			}
		},
	},
	{
		// ALTER TABLE [...] RENAME TO new (table rename, not column)
		pattern: regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` + optSchema + `(` + ident + `)\s+RENAME\s+TO\s+(` + ident + `)`),
		build: func(m []string) *DetectedDDL {
			newName := unquoteIdent(m[2])
			return &DetectedDDL{
				Action:    "rename_table",
				TableName: newName,
				Detail: map[string]any{
					"source":   "sql",
					"old_name": unquoteIdent(m[1]),
				},
			}
		},
	},
	{
		// ALTER TABLE [...] ALTER COLUMN name (TYPE | SET / DROP NOT NULL | SET / DROP DEFAULT)
		pattern: regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` + optSchema + `(` + ident + `)\s+ALTER\s+COLUMN\s+(` + ident + `)\s+(TYPE\s+([A-Za-z][A-Za-z0-9_ ]*)|SET\s+NOT\s+NULL|DROP\s+NOT\s+NULL|SET\s+DEFAULT|DROP\s+DEFAULT)`),
		build: func(m []string) *DetectedDDL {
			col := unquoteIdent(m[2])
			// m[3] is the full sub-operation text (e.g. "TYPE varchar",
			// "SET NOT NULL", "DROP DEFAULT"). For TYPE we want kind=
			// "type" and the type itself goes in new_type (m[4]); for
			// the others we collapse the multi-word kind to a single
			// snake_case token ("SET NOT NULL" -> "set_not_null").
			detail := map[string]any{"source": "sql"}
			if len(m) > 4 && strings.TrimSpace(m[4]) != "" {
				detail["kind"] = "type"
				detail["new_type"] = strings.TrimSpace(m[4])
			} else {
				detail["kind"] = strings.ToLower(strings.Join(strings.Fields(m[3]), "_"))
			}
			return &DetectedDDL{
				Action:     "alter_column",
				TableName:  unquoteIdent(m[1]),
				ColumnName: strPtr(col),
				Detail:     detail,
			}
		},
	},
	{
		// CREATE [UNIQUE] INDEX [CONCURRENTLY] [IF NOT EXISTS] [name] ON [schema.]table (...)
		pattern: regexp.MustCompile(`(?is)\bCREATE\s+(?:UNIQUE\s+)?INDEX(?:\s+CONCURRENTLY)?(?:\s+IF\s+NOT\s+EXISTS)?(?:\s+(` + ident + `))?\s+ON\s+(?:ONLY\s+)?` + optSchema + `(` + ident + `)`),
		build: func(m []string) *DetectedDDL {
			detail := map[string]any{"source": "sql"}
			if idxName := unquoteIdent(m[1]); idxName != "" {
				detail["index_name"] = idxName
			}
			return &DetectedDDL{
				Action:    "create_index",
				TableName: unquoteIdent(m[2]),
				Detail:    detail,
			}
		},
	},
	{
		// DROP INDEX [CONCURRENTLY] [IF EXISTS] [schema.]name
		pattern: regexp.MustCompile(`(?is)\bDROP\s+INDEX(?:\s+CONCURRENTLY)?(?:\s+IF\s+EXISTS)?\s+` + optSchema + `(` + ident + `)`),
		build: func(m []string) *DetectedDDL {
			idxName := unquoteIdent(m[1])
			return &DetectedDDL{
				Action:    "drop_index",
				TableName: idxName, // mirrors the event-trigger fallback: target table not recoverable from DROP alone
				Detail: map[string]any{
					"source":     "sql",
					"index_name": idxName,
					"note":       "table_name contains the index name; target table is not resolvable from DROP INDEX alone",
				},
			}
		},
	},
}

// stripStringsAndCommentsForDetect blanks out string literals,
// double-quoted identifiers (replaced with spaces so identifier
// detection still gets a clean token via the regex), line comments,
// block comments, and dollar-quoted strings. Mirrors the cron
// package's scanner but local to query to avoid a package cycle
// (cron already imports query).
//
// We replace stripped content with spaces (preserving length) so
// regex offsets stay aligned with the original. Double-quoted
// identifiers are NOT stripped — they're legitimate DDL syntax —
// but they're case-preserved by the `(?i)` flag elsewhere.
func stripStringsAndCommentsForDetect(sql string) string {
	out := make([]byte, 0, len(sql))
	emit := func(c byte) { out = append(out, c) }
	skip := func(n int) {
		for i := 0; i < n; i++ {
			out = append(out, ' ')
		}
	}

	s := sql
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '\'':
			// Single-quoted string. Postgres allows '' as an escape.
			j := i + 1
			for j < n {
				if s[j] == '\'' {
					if j+1 < n && s[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			skip(j - i)
			i = j
		case c == '-' && i+1 < n && s[i+1] == '-':
			// Line comment to EOL.
			j := i + 2
			for j < n && s[j] != '\n' {
				j++
			}
			skip(j - i)
			i = j
		case c == '/' && i+1 < n && s[i+1] == '*':
			// Block comment (nest-aware to match Postgres).
			j := i + 2
			depth := 1
			for j < n && depth > 0 {
				if j+1 < n && s[j] == '/' && s[j+1] == '*' {
					depth++
					j += 2
					continue
				}
				if j+1 < n && s[j] == '*' && s[j+1] == '/' {
					depth--
					j += 2
					continue
				}
				j++
			}
			skip(j - i)
			i = j
		case c == '$':
			// Dollar-quoted string ($tag$ ... $tag$). Tag is optional.
			tagEnd := i + 1
			for tagEnd < n && isDollarTagChar(s[tagEnd], tagEnd == i+1) { //nolint:staticcheck // isDollarTagChar lives in multistmt.go
				tagEnd++
			}
			if tagEnd >= n || s[tagEnd] != '$' {
				emit(c)
				i++
				continue
			}
			tag := s[i : tagEnd+1]
			closer := strings.Index(s[tagEnd+1:], tag)
			if closer < 0 {
				skip(n - i)
				i = n
				continue
			}
			end := tagEnd + 1 + closer + len(tag)
			skip(end - i)
			i = end
		default:
			emit(c)
			i++
		}
	}
	return string(out)
}

