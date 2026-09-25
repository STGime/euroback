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

// errMultiStatement is returned when a cron SQL action contains more than
// one statement. The previous `SET search_path TO X; <action>` pattern
// concatenated multiple statements and let the second one drop into any
// schema — this rejects that shape.
var errMultiStatement = errors.New("cron sql action must be a single statement")

// validateCronSQLAction applies the same checks as the SQL endpoints
// (internal/query/sql_handler.go) to a user-supplied `action_type='sql'`
// statement, so cron is not a weaker way in to the shared database role:
//   - single statement only (GHSA-fjjq-cqq9-q793)
//   - no qualified reference outside the tenant's own schema, using the
//     shared tokenizer (quoted / unicode-escaped identifiers included)
//     and the SDK allowlist — cron runs as the runtime role, not as the
//     platform developer
//   - no system-catalog references (pg_stat_activity etc.)
//   - no role / session / privilege statements (SET ROLE, GRANT, …)
//
// It does NOT validate that the SQL is syntactically valid or that the
// referenced tables exist — Postgres surfaces those when it runs.
func validateCronSQLAction(action, schema string) error {
	trimmed := strings.TrimSpace(action)
	if trimmed == "" {
		return errors.New("empty cron sql action")
	}
	if query.HasMultipleStatements(trimmed) {
		return errMultiStatement
	}
	if err := query.ValidateNoCrossSchemaRefsOpts(trimmed, schema, query.CrossSchemaOptions{
		AllowedPublicNames: query.SDKPublicAllowlist,
	}); err != nil {
		return err
	}
	if err := query.ValidateNoCatalogRefs(trimmed); err != nil {
		return err
	}
	return query.ValidateNoPrivilegeStatements(trimmed)
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
