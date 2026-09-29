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
func viewerHidesInternal(ctx context.Context) bool {
	return PlatformRoleFromContext(ctx) == "viewer"
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
