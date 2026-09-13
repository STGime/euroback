package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errDeniedSQL is the uniform rejection for the raw /sql guards. It
// deliberately does NOT echo the offending table/column name, so error
// output stays constant and untied to caller input.
var errDeniedSQL = errors.New("this query references a restricted system table or column and is not allowed on the SQL endpoint")

// sensitiveColumns lists columns on the per-tenant *system* tables that
// must never be exposed through the generic /v1/db data API to
// client-facing callers (an anonymous request or an end-user JWT, both
// carried under the public/anon key).
//
// Why this is needed. The tenant `users` table is gated only by
// row-level RLS (the user_self_access policy created in
// provision_tenant), which lets a signed-in end-user read *their own*
// row — including password_hash. RLS is row-level and cannot mask a
// column, and the engine's ValidateColumns is an existence /
// SQL-injection guard, not an access control. So `?select=password_hash`
// (or `select=*`) on the user's own row returned the hash.
//
// Why enforcing here is safe for auth. Sign-in / sign-up do NOT go
// through this engine — they use direct queries in
// internal/enduser/auth_service.go under the service-role GUC
// (app.end_user_role='service'), on the same eurobase_gateway DB role.
// Because the role is shared, a column-level REVOKE would also break
// auth; the correct layer to stop the client-facing leak is here, in
// the data API, leaving the auth path untouched.
//
// The server-side service (secret) key is exempt — it is trusted
// server-side access and is never handed to clients.
//
// Keyed by unqualified table name: every tenant schema shares the same
// system-table shape from provision_tenant.
//
// SURFACE NOTE: this map guards the typed REST data API
// (/v1/db/{table} — SelectRows / AggregateQuery / relations /
// Insert / Update). The raw SDK SQL endpoint (/v1/db/sql) is a
// SEPARATE surface guarded by guardSDKSQL* below, and the RPC endpoint
// (/v1/db/rpc/{function} → CallFunction) is NOT guarded here at all —
// a tenant-authored SECURITY DEFINER function that reads a system
// table would sidestep this denylist. Keep that in mind before
// assuming "sensitive columns are covered everywhere."
var sensitiveColumns = map[string]map[string]bool{
	"users":          {"password_hash": true},
	"refresh_tokens": {"token_hash": true},
	"email_tokens":   {"token_hash": true},
	"vault_secrets":  {"secret": true, "nonce": true},
}

// sqlPathDeniedColumns is the denied-column set for the raw SDK SQL
// endpoint (/v1/db/sql). It is intentionally NARROWER than
// sensitiveColumns: on the raw-SQL path the table a column belongs to
// is not known, so the guard scans column *names* across the whole
// statement. Only password_hash qualifies:
//   - reachable — refresh_tokens / email_tokens / vault_secrets are
//     service-only via RLS, so a public / end-user caller reads zero
//     rows from them regardless of the columns named; password_hash on
//     users is the one sensitive column a non-service caller can reach
//     (the user's own row, via the user_self_access policy);
//   - specific — a bare name scan for the generic "secret" / "nonce"
//     would routinely collide with tenants' own columns.
//
// Conservative trade-off: a tenant app column also literally named
// password_hash, queried via raw /sql under the public key, is
// rejected. Such callers should use the typed REST endpoint (keyed on
// table name, so their own table is not on the denylist) or a service
// key.
//
// The column scan is only the FIRST of two layers on /sql. It catches
// a value that is named (directly, aliased, qualified, or in a
// subquery) — but NOT a whole row smuggled out under an innocuous name,
// e.g. `SELECT to_jsonb(u) FROM users u`, `SELECT row_to_json(u) ...`,
// or the bare row `SELECT u FROM users u`. There the identifier stream
// is {to_jsonb, u, users} and the output column is "to_jsonb" — no
// password_hash token anywhere — yet the serialized value contains the
// full row. Column-name filtering cannot win against row-typed / JSON
// wrapping on a raw-SQL endpoint, so sqlPathDeniedTables below closes
// it at the table level.
var sqlPathDeniedColumns = map[string]bool{
	"password_hash": true,
}

// sqlPathDeniedTables blocks the raw SDK SQL endpoint from referencing
// the per-tenant system tables AT ALL (not just their sensitive
// columns). This is the robust layer: on a raw-SQL surface a row can be
// exfiltrated without ever naming a column — to_jsonb(u), row_to_json(u),
// a bare row-typed `SELECT u`, hstore(u), a composite cast, etc. — so
// the only reliable guard is to refuse the statement if it touches one
// of these tables.
//
// This is safe on the SDK path: a public / end-user-JWT caller has no
// legitimate raw-SQL use for the auth system tables. The typed REST
// endpoint exposes exactly the non-sensitive columns each one needs
// (with password_hash stripped), and everything the auth flow itself
// needs lives in internal/enduser/auth_service.go under the service-
// role GUC. The service key is exempt (see guardSDKSQLTables). Trade-off
// (documented): `SELECT id, email FROM users` via raw /sql under the
// public key is now refused — use the REST path for that.
var sqlPathDeniedTables = map[string]bool{
	"users":          true,
	"refresh_tokens": true,
	"email_tokens":   true,
	"vault_secrets":  true,
}

// serviceKeyExempt reports whether the caller may see sensitive
// columns. Only the server-side secret key is trusted; the public key
// (anonymous or end-user JWT) is not.
func serviceKeyExempt(ctx context.Context) bool {
	return KeyTypeFromContext(ctx) == "secret"
}

// deniedColumns returns the sensitive-column set for a table, or nil.
func deniedColumns(tableName string) map[string]bool {
	return sensitiveColumns[tableName]
}

// checkDeniedColumns rejects an explicit reference to a sensitive
// column by a non-service caller (in select / filter / order /
// aggregate / insert / update). A "*" entry is ignored — the wildcard
// is handled by stripDeniedColumns so a plain `select=*` on an ordinary
// table keeps working; only an explicitly-named sensitive column is
// rejected, which also closes the filter-as-oracle vector (e.g.
// ?password_hash=like.$2a$*).
func checkDeniedColumns(ctx context.Context, tableName string, cols []string) error {
	if serviceKeyExempt(ctx) {
		return nil
	}
	denied := deniedColumns(tableName)
	if denied == nil {
		return nil
	}
	for _, c := range cols {
		if denied[c] {
			return fmt.Errorf("column %q on %q is not accessible via the data API", c, tableName)
		}
	}
	return nil
}

// checkDeniedRelation rejects embedding a protected table when the
// requested columns would expose a sensitive one — including the
// wildcard `table(*)`, which pulls every column via row_to_json and so
// cannot be reliably stripped afterwards. Naming only safe columns
// explicitly (e.g. users(id,email)) still works.
func checkDeniedRelation(ctx context.Context, relTable string, cols []string) error {
	if serviceKeyExempt(ctx) {
		return nil
	}
	denied := deniedColumns(relTable)
	if denied == nil {
		return nil
	}
	for _, c := range cols {
		if c == "*" {
			return fmt.Errorf("embedding all columns of %q is not allowed; select specific columns", relTable)
		}
		if denied[c] {
			return fmt.Errorf("column %q on %q is not accessible via the data API", c, relTable)
		}
	}
	return nil
}

// stripDeniedColumns removes sensitive columns from result rows for a
// non-service caller. This is the catch-all for the cases where a
// sensitive column is returned without being named: `select=*`, an
// omitted select, and INSERT / UPDATE ... RETURNING *.
//
// It scrubs ONLY the base table's own columns, and that is sufficient
// because the two ways a nested/embedded value could appear are both
// blocked earlier: an explicitly named sensitive relation column and
// the `table(*)` wildcard are rejected by checkDeniedRelation before
// the query runs. If that relation guard is ever relaxed, this
// base-table-only strip would leave a nested-object gap — keep the two
// in step.
func stripDeniedColumns(ctx context.Context, tableName string, rows []map[string]interface{}) {
	if serviceKeyExempt(ctx) {
		return
	}
	denied := deniedColumns(tableName)
	if denied == nil {
		return
	}
	for _, row := range rows {
		for col := range denied {
			delete(row, col)
		}
	}
}

// guardSDKSQLInput rejects a raw SDK-path SQL statement that references
// a denied column, for a non-service caller. It scans identifier tokens
// only (scanIdentifiersAndDots skips string literals, comments, and
// dollar-quoted bodies), so `SELECT 'password_hash'` or a comment does
// not false-positive. Because a column cannot be returned without being
// named somewhere in the statement, this catches explicit refs,
// aliases (`password_hash AS x` — the reviewer's alias bypass of an
// output-only check), and subqueries alike. Runs BEFORE execution so
// the value is never read.
func guardSDKSQLInput(ctx context.Context, sql string) error {
	if serviceKeyExempt(ctx) {
		return nil
	}
	for _, tok := range scanIdentifiersAndDots(sql) {
		if tok.kind != tokIdent {
			continue
		}
		if sqlPathDeniedColumns[strings.ToLower(tok.value)] {
			return errDeniedSQL
		}
	}
	return nil
}

// guardSDKSQLTables rejects a raw SDK-path statement that references any
// per-tenant system table, for a non-service caller. This is the layer
// that closes the row-typed / JSON-wrap bypass (to_jsonb(u),
// row_to_json(u), bare `SELECT u`, composite casts, …) that the
// column-name scans cannot see, because it keys on the table reference
// rather than on any column name. Identifier-token scan, so a matching
// string literal or comment does not false-positive.
func guardSDKSQLTables(ctx context.Context, sql string) error {
	if serviceKeyExempt(ctx) {
		return nil
	}
	for _, tok := range scanIdentifiersAndDots(sql) {
		if tok.kind != tokIdent {
			continue
		}
		if sqlPathDeniedTables[strings.ToLower(tok.value)] {
			return errDeniedSQL
		}
	}
	return nil
}

// guardSDKSQLOutput rejects a raw SDK-path result whose returned column
// names include a denied column, for a non-service caller. This is the
// backstop for `SELECT *`, which returns the column under its real name
// without ever naming it in the statement (so guardSDKSQLInput can't
// see it). Rejecting rather than stripping, because a raw result set
// can't be reliably mapped back to its source columns once aliased or
// computed.
func guardSDKSQLOutput(ctx context.Context, columns []string) error {
	if serviceKeyExempt(ctx) {
		return nil
	}
	for _, c := range columns {
		if sqlPathDeniedColumns[strings.ToLower(c)] {
			return errDeniedSQL
		}
	}
	return nil
}
