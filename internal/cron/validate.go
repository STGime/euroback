package cron

import (
	"errors"
	"regexp"
	"strings"

	"github.com/eurobase/euroback/internal/query"
)

// validRPCNameRe matches safe SQL identifiers (the same shape the DDL
// validator uses elsewhere).
var validRPCNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validFunctionNameRe matches safe edge-function names. Edge functions
// use kebab-case (`purge-expired-images`) so we allow hyphens here. The
// expression is reused via validateFunctionName in service.go.
var validFunctionNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// forbiddenSchemaRe matches qualified references to schemas that a tenant
// cron job has no legitimate need to touch. The list:
//
//   - public — platform tables (projects, api_keys, platform_users, …) live here
//   - pg_catalog — Postgres metadata
//   - information_schema — Postgres metadata
//   - tenant_… — other tenants' schemas
//
// The pattern catches `schema.relation` where `schema` is one of the above.
// It is run after stripping string literals and comments so a literal like
// `'public.api_keys is private'` doesn't falsely trip.
//
// This is defense-in-depth on top of `SET LOCAL search_path TO {tenant}`
// which already locks down unqualified resolution. Both layers have to be
// bypassed to reach another schema.
var forbiddenSchemaRe = regexp.MustCompile(
	`(?i)\b(public|pg_catalog|information_schema|tenant_[a-z0-9_]+)\b\s*\.`,
)

// errMultiStatement is returned when a cron SQL action contains more than
// one statement. The previous `SET search_path TO X; <action>` pattern
// concatenated multiple statements and let the second one drop into any
// schema — this rejects that shape.
var errMultiStatement = errors.New("cron sql action must be a single statement")

// errForbiddenSchema is returned when a cron SQL action references a
// schema outside the calling tenant.
var errForbiddenSchema = errors.New("cron sql action references a forbidden schema (public, pg_catalog, information_schema, or another tenant)")

// validateCronSQLAction guards the user-supplied `action_type='sql'` text
// against the privilege escalation paths that closed advisory
// GHSA-fjjq-cqq9-q793. It rejects:
//   - empty input
//   - multi-statement input
//   - any reference to a forbidden schema (after string/comment stripping)
//
// It does NOT validate that the SQL is syntactically valid or that the
// referenced tables exist — Postgres will surface those errors when the
// statement runs.
func validateCronSQLAction(action string) error {
	trimmed := strings.TrimSpace(action)
	if trimmed == "" {
		return errors.New("empty cron sql action")
	}
	if query.HasMultipleStatements(trimmed) {
		return errMultiStatement
	}
	if forbiddenSchemaRe.MatchString(stripStringsAndComments(trimmed)) {
		return errForbiddenSchema
	}
	return nil
}

// validateCronRPCName accepts only safe identifiers — letters, digits,
// underscores, leading non-digit. Function lookup happens through the
// tenant search_path, so the name can refer to a tenant-owned function
// only.
func validateCronRPCName(name string) error {
	if !validRPCNameRe.MatchString(strings.TrimSpace(name)) {
		return errors.New("invalid rpc function name (use letters, digits, underscores)")
	}
	return nil
}

// stripStringsAndComments returns the SQL with single-quoted strings,
// double-quoted identifiers, dollar-quoted strings, line comments, and
// block comments replaced by spaces. The resulting text preserves the
// non-quoted structure so an identifier-pair regex can run without false
// positives on quoted text. Mirrors the scanner in
// internal/query/multistmt.go.
func stripStringsAndComments(sql string) string {
	out := make([]byte, 0, len(sql))
	emit := func(c byte) { out = append(out, c) }
	skip := func(b []byte) {
		for range b {
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
			skip([]byte(s[i:j]))
			i = j
		case c == '"':
			j := i + 1
			for j < n {
				if s[j] == '"' {
					if j+1 < n && s[j+1] == '"' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			skip([]byte(s[i:j]))
			i = j
		case c == '-' && i+1 < n && s[i+1] == '-':
			j := i + 2
			for j < n && s[j] != '\n' {
				j++
			}
			skip([]byte(s[i:j]))
			i = j
		case c == '/' && i+1 < n && s[i+1] == '*':
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
			skip([]byte(s[i:j]))
			i = j
		case c == '$':
			tagEnd := i + 1
			for tagEnd < n && isDollarTagChar(s[tagEnd], tagEnd == i+1) {
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
				skip([]byte(s[i:]))
				i = n
				continue
			}
			end := tagEnd + 1 + closer + len(tag)
			skip([]byte(s[i:end]))
			i = end
		default:
			emit(c)
			i++
		}
	}
	return string(out)
}

func isDollarTagChar(c byte, first bool) bool {
	if first {
		return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
