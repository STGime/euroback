package dbprovider

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TenantDB is where a project's tenant data lives (#676, #677): the shared
// cluster, or a Team project's dedicated instance.
type TenantDB struct {
	// Dedicated is false for a project on the shared cluster.
	Dedicated bool
	// ID is project_databases.id — the per-instance password subject
	// (tenantlogin.DedicatedSubject).
	ID       string
	Host     string
	Port     int
	Database string
}

// ErrTenantDBNotReady: the project has a dedicated database that can't be
// used right now (provisioning, restoring, upgrading, maintenance). Never
// a reason to use the shared cluster instead.
var ErrTenantDBNotReady = errors.New("the project's database is not ready")

// TenantDBQuerier is satisfied by *pgxpool.Pool and pgx.Tx.
type TenantDBQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ResolveTenantDB asks public.runner_get_tenant_db (migration 000131) —
// the one routing rule the functions runner uses too — where projectID's
// tenant data lives. q must be able to execute that function: the
// runner's login, or eurobase_developer / eurobase_migrator (owner).
// Returns ErrTenantDBNotReady (wrapped, with the state) for a dedicated
// database that isn't serving; never falls back to the shared cluster.
func ResolveTenantDB(ctx context.Context, q TenantDBQuerier, projectID string) (TenantDB, error) {
	rows, err := q.Query(ctx,
		`SELECT coalesce(id::text, ''), coalesce(host, ''), coalesce(port, 0), coalesce(database_name, ''), state
		   FROM public.runner_get_tenant_db($1::uuid)`, projectID)
	if err != nil {
		return TenantDB{}, fmt.Errorf("tenant database lookup: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return TenantDB{}, fmt.Errorf("tenant database lookup: %w", err)
		}
		return TenantDB{}, nil // shared cluster
	}
	var t TenantDB
	var state string
	if err := rows.Scan(&t.ID, &t.Host, &t.Port, &t.Database, &state); err != nil {
		return TenantDB{}, fmt.Errorf("tenant database lookup: %w", err)
	}
	if state != string(StateActive) || t.ID == "" || t.Host == "" || t.Port == 0 || t.Database == "" {
		return TenantDB{}, fmt.Errorf("%w (%s)", ErrTenantDBNotReady, state)
	}
	t.Dedicated = true
	return t, rows.Err()
}

// CanResolveTenantDB reports whether q's role may execute
// public.runner_get_tenant_db — checked at startup so a missing developer
// pool (DATABASE_URL_DEVELOPER) shows up as one clear error instead of
// every sql / rpc cron job failing its lookup.
func CanResolveTenantDB(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT has_function_privilege('public.runner_get_tenant_db(uuid)', 'EXECUTE')`).Scan(&ok)
	return ok, err
}
