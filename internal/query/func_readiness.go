package query

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Pre-flip readiness for SDK_FUNC_LOGIN (step 4). Before SDK customer SQL
// moves onto the per-project `<schema>_func` login, every tenant schema must
// satisfy two invariants, or the flip changes behaviour:
//
//  1. No application table is still owned by eurobase_gateway. The gateway is
//     the owner of such a table, so RLS is BYPASSED for SDK traffic today;
//     running as `_func` (a non-owner) would begin enforcing RLS — a
//     correctness change. Reassign those to `_ddl` first (and settle each
//     table's service policy).
//  2. `_func` has full DML (SELECT/INSERT/UPDATE/DELETE) on every application
//     table, or customer SQL fails with permission denied once routed.
//
// "Application table" matches migration 000136's converge_tenant_ownership:
// a table (relkind 'r'/'p') that is not one of the platform-managed system
// tables. scripts/ops/check-sdk-func-readiness.sh runs the same two checks
// against prod — keep the SQL in sync.

// tenantSystemTables is the platform-managed set excluded from "application
// table" — identical to converge_tenant_ownership's v_system_tables.
var tenantSystemTables = []string{
	"users", "user_identities", "refresh_tokens", "email_tokens",
	"storage_objects", "storage_shared_prefixes", "vault_secrets",
}

// sqlGatewayOwnedAppTables lists application tables in a schema ($1) still
// owned by eurobase_gateway ($2 = system-table names to exclude).
const sqlGatewayOwnedAppTables = `
SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_roles r ON r.oid = c.relowner
WHERE n.nspname = $1
  AND c.relkind IN ('r', 'p')
  AND r.rolname = 'eurobase_gateway'
  AND NOT (c.relname = ANY($2))
ORDER BY 1`

// sqlFuncMissingDML lists application tables in a schema ($1) on which the
// `_func` role ($3) lacks any of SELECT/INSERT/UPDATE/DELETE ($2 = system
// tables to exclude). The role is joined by name and privileges are checked
// by OID so a missing role yields no rows rather than an error (the name
// form of has_table_privilege throws on an unknown role, and the planner may
// evaluate it before the schema filter).
const sqlFuncMissingDML = `
SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_roles fr ON fr.rolname = $3
WHERE n.nspname = $1
  AND c.relkind IN ('r', 'p')
  AND NOT (c.relname = ANY($2))
  AND NOT (
      has_table_privilege(fr.oid, c.oid, 'SELECT')
  AND has_table_privilege(fr.oid, c.oid, 'INSERT')
  AND has_table_privilege(fr.oid, c.oid, 'UPDATE')
  AND has_table_privilege(fr.oid, c.oid, 'DELETE')
  )
ORDER BY 1`

type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// TenantFuncReadiness returns the two pre-flip gates for a tenant schema:
// application tables still owned by eurobase_gateway (must be empty), and
// application tables on which `<schema>_func` lacks full DML (must be empty).
// Both empty = the schema is safe to serve SDK customer SQL on its `_func`
// login.
func TenantFuncReadiness(ctx context.Context, q rowQuerier, schema string) (gatewayOwned, funcMissingDML []string, err error) {
	gatewayOwned, err = collectNames(ctx, q, sqlGatewayOwnedAppTables, schema, tenantSystemTables)
	if err != nil {
		return nil, nil, err
	}
	funcMissingDML, err = collectNames(ctx, q, sqlFuncMissingDML, schema, tenantSystemTables, schema+"_func")
	if err != nil {
		return nil, nil, err
	}
	return gatewayOwned, funcMissingDML, nil
}

func collectNames(ctx context.Context, q rowQuerier, sql string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
