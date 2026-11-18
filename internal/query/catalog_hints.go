package query

import "strings"

// Hints for refused catalog references.
//
// The catalog guards (ValidateNoCatalogRefs, the cross-schema check for
// pg_catalog / information_schema, the migrations validator) refuse SQL
// that reads the system catalog. Most such SQL is a developer, or their
// coding agent, trying to look at their own schema: a function's source,
// a table's policies, its constraints. The platform answers those
// questions itself, so a refusal names where to look instead. Without a
// pointer, agents tend to retry the same queries.
//
// A hint only names platform tools and endpoints; it says nothing about
// the refused object beyond the name the caller already typed.

const schemaAPI = "GET /platform/projects/<project-id>/schema"

const (
	hintFunctions = "your functions and their definitions are available with the MCP tools listDbFunctions / getDbFunction, or " + schemaAPI + "/functions (developer role)"
	hintPolicies  = "a table's RLS policies are listed in the console (Database, the table's policies) and by " + schemaAPI + "/tables/<table>/policies (developer role)"
	hintTriggers  = "a table's triggers are listed by the MCP tool describeTable and " + schemaAPI + "/tables/<table>/triggers (developer role); read the trigger function with getDbFunction"
	hintTables    = "your tables, columns, primary / unique / foreign keys, indexes, triggers and RLS status are available with the MCP tools listTables / describeTable, or " + schemaAPI
	hintObjects   = "your views, materialized views, sequences and types are available with the MCP tool listSchemaObjects, or " + schemaAPI + "/objects"
	hintAccess    = "roles and grants are managed by the platform; each table's RLS status is in the console (Database) and " + schemaAPI + "/rls-audit"
)

// catalogHints maps a pg_-prefixed catalog name (lower case) to its hint.
var catalogHints = map[string]string{
	// functions
	"pg_proc":                            hintFunctions,
	"pg_get_functiondef":                 hintFunctions,
	"pg_get_function_arguments":          hintFunctions,
	"pg_get_function_identity_arguments": hintFunctions,
	"pg_get_function_result":             hintFunctions,
	"pg_language":                        hintFunctions,
	// policies
	"pg_policies": hintPolicies,
	"pg_policy":   hintPolicies,
	// triggers
	"pg_trigger":        hintTriggers,
	"pg_get_triggerdef": hintTriggers,
	// tables, columns, keys, indexes. CHECK constraint expressions
	// (pg_get_constraintdef, information_schema.check_constraints) and
	// comments (pg_description) aren't returned by the schema API, so
	// those get no hint rather than a wrong one.
	"pg_class":        hintTables,
	"pg_tables":       hintTables,
	"pg_namespace":    hintTables,
	"pg_attribute":    hintTables,
	"pg_attrdef":      hintTables,
	"pg_constraint":   hintTables,
	"pg_index":        hintTables,
	"pg_indexes":      hintTables,
	"pg_get_indexdef": hintTables,
	"pg_get_expr":     hintTables,
	// views, sequences, types
	"pg_views":       hintObjects,
	"pg_matviews":    hintObjects,
	"pg_get_viewdef": hintObjects,
	"pg_sequence":    hintObjects,
	"pg_sequences":   hintObjects,
	"pg_type":        hintObjects,
	"pg_enum":        hintObjects,
	// roles and grants
	"pg_roles":        hintAccess,
	"pg_user":         hintAccess,
	"pg_authid":       hintAccess,
	"pg_auth_members": hintAccess,
	"pg_has_role":     hintAccess,
	"pg_default_acl":  hintAccess,
}

// informationSchemaHints maps an information_schema view (lower case) to
// its hint.
var informationSchemaHints = map[string]string{
	"tables":                  hintTables,
	"columns":                 hintTables,
	"table_constraints":       hintTables,
	"key_column_usage":        hintTables,
	"constraint_column_usage": hintTables,
	"referential_constraints": hintTables,
	"routines":                hintFunctions,
	"parameters":              hintFunctions,
	"triggers":                hintTriggers,
	"views":                   hintObjects,
	"sequences":               hintObjects,
	"table_privileges":        hintAccess,
	"column_privileges":       hintAccess,
	"routine_privileges":      hintAccess,
	"usage_privileges":        hintAccess,
	"role_table_grants":       hintAccess,
	"role_column_grants":      hintAccess,
	"role_routine_grants":     hintAccess,
	"role_usage_grants":       hintAccess,
	"applicable_roles":        hintAccess,
	"enabled_roles":           hintAccess,
}

// hintSuffix is appended to a refusal message; "" when there is no hint.
func hintSuffix(hint string) string {
	if hint == "" {
		return ""
	}
	return ". Hint: " + hint
}

// catalogRefHint returns the hint for a refused reference: name is the
// catalog object (bare pg_ name, or the name after pg_catalog. /
// information_schema.), schema the qualifying schema ("" when bare).
func catalogRefHint(schema, name string) string {
	schema = strings.ToLower(schema)
	name = strings.ToLower(name)
	switch schema {
	case "information_schema":
		return informationSchemaHints[name]
	case "", "pg_catalog":
		return catalogHints[name]
	}
	return ""
}

// withoutHint returns a refusal message without its hint.
func withoutHint(msg string) string {
	if i := strings.Index(msg, ". Hint: "); i >= 0 {
		return msg[:i]
	}
	return msg
}
