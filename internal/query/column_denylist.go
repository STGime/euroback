package query

import (
	"context"
	"fmt"
)

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
var sensitiveColumns = map[string]map[string]bool{
	"users":          {"password_hash": true},
	"refresh_tokens": {"token_hash": true},
	"email_tokens":   {"token_hash": true},
	"vault_secrets":  {"secret": true, "nonce": true},
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
// omitted select, and INSERT / UPDATE ... RETURNING *. Named access and
// wildcard relation embedding are already rejected up front, so this
// only ever has to scrub the base table's own columns.
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
