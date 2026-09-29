package query

import (
	"context"
	"errors"
	"fmt"
)

// internalTenantTables are the tables provision_tenant creates in every
// tenant schema for the platform itself: end users and their identities,
// auth tokens, the vault, and storage bookkeeping. Each has its own
// console page or API (Users, Vault, Storage) with its own role
// requirement; the console's table browser hides them.
var internalTenantTables = map[string]bool{
	"users":                   true,
	"user_identities":         true,
	"refresh_tokens":          true,
	"email_tokens":            true,
	"vault_secrets":           true,
	"storage_objects":         true,
	"storage_shared_prefixes": true,
}

// IsInternalTenantTable reports whether table is one of the platform's own
// tables in a tenant schema.
func IsInternalTenantTable(table string) bool {
	return internalTenantTables[table]
}

// ErrInternalTable: a Viewer read an internal table through the platform
// data API (403).
var ErrInternalTable = errors.New("internal table")

// viewerHidesInternal reports whether the caller is a platform Viewer, who
// gets project data only: no internal tables, in the data API or the
// schema listing. A console Viewer has no SQL editor and no Users / Vault
// page, so a Viewer (console session or token) must not reach those rows
// through the data API either (#702). Developer and up can read every
// table with SQL in the console, so they keep data-API access.
//
// Fail closed: every platform role other than developer / admin / owner
// (a future lower role, an unexpected value) is restricted. "" is the SDK
// path (no platform role), guarded by RLS and the column denylist instead.
func viewerHidesInternal(ctx context.Context) bool {
	switch PlatformRoleFromContext(ctx) {
	case "", "developer", "admin", "owner":
		return false
	default:
		return true
	}
}

// RoleSeesInternalTables reports whether a project role may read the
// internal tables (developer and up) — for surfaces outside this package
// such as realtime subscriptions.
func RoleSeesInternalTables(role string) bool {
	switch role {
	case "developer", "admin", "owner":
		return true
	}
	return false
}

// InternalTenantTables returns a copy of the internal-table set.
func InternalTenantTables() map[string]bool {
	out := make(map[string]bool, len(internalTenantTables))
	for t := range internalTenantTables {
		out[t] = true
	}
	return out
}

// PublishableRow returns a copy of a row for realtime events without the
// credential columns of the internal tables (password / token hashes,
// vault ciphertext): events go to every subscriber of the channel,
// whoever wrote the row.
func PublishableRow(table string, row map[string]interface{}) map[string]interface{} {
	denied := SensitiveSystemColumns(table)
	if denied == nil || row == nil {
		return row
	}
	out := make(map[string]interface{}, len(row))
	for k, v := range row {
		if !denied[k] {
			out[k] = v
		}
	}
	return out
}

// checkInternalTableRead refuses a platform Viewer's read of an internal
// table (also as an embedded relation).
func checkInternalTableRead(ctx context.Context, table string) error {
	if viewerHidesInternal(ctx) && internalTenantTables[table] {
		return fmt.Errorf("%w: %q needs a higher project role than Viewer", ErrInternalTable, table)
	}
	return nil
}

// visibleSchemaTables drops the internal tables from a schema listing for a
// platform Viewer.
func visibleSchemaTables(ctx context.Context, tables []string) []string {
	if !viewerHidesInternal(ctx) {
		return tables
	}
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		if !internalTenantTables[t] {
			out = append(out, t)
		}
	}
	return out
}
