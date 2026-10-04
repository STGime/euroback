// Package tenantconn opens a database connection that logs in as a
// project's own `<schema>_func` role, for running customer SQL — the model
// edge functions and cron already use, now shared by every customer-SQL
// path (SDK SQL, data API, SQL editor, cron, …).
//
// Why a per-project login: a shared platform login can reach every tenant
// schema, so customer SQL on it is a cross-tenant surface. A `<schema>_func`
// connection is a member of exactly one tenant, so even a statement that
// resets its role lands back on the same tenant.
//
// This package owns the security-critical seam: resolving where a project's
// data lives (shared cluster vs its dedicated Team instance), logging in as
// the right role with the derived password, and pinning string-literal
// parsing so the server tokenizes the way the SQL guards do. Each caller
// applies its own session settings (search_path, statement_timeout,
// read-only, RLS identity) via the setup callback.
package tenantconn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/jackc/pgx/v5"
)

// Sentinel errors. Messages are safe to show a tenant (no host / role /
// driver detail); the detail is logged.
var (
	// ErrNotConfigured: per-tenant logins aren't set up on this process
	// (missing FUNC_PASSWORD_SECRET / base config).
	ErrNotConfigured = errors.New("per-project database logins are not configured on this service")
	// ErrNotReady: a Team project's dedicated database isn't serving yet
	// (provisioning / restoring / upgrading). Retry later; never shared.
	ErrNotReady = errors.New("the project's database is not ready; try again shortly")
	// ErrRouting: the project's database location couldn't be resolved.
	ErrRouting = errors.New("could not resolve the project's database")
	// ErrConnect: logging in as the project's role failed (it may still be
	// provisioning).
	ErrConnect = errors.New("could not connect as the project's database role; it may still be provisioning")
)

// Resolver builds and opens per-project `<schema>_func` connections.
type Resolver struct {
	base        *pgx.ConnConfig            // this service's own conn config (host, db, TLS)
	secret      []byte                     // FUNC_PASSWORD_SECRET
	routeDB     dbprovider.TenantDBQuerier // nil = every project on the shared cluster (tests)
	appName     string                     // application_name, e.g. "eurobase-cron"
	connTimeout time.Duration
}

// NewResolver returns a Resolver. base and secret are required to open any
// connection (ConnConfig / RunInTx return ErrNotConfigured without them).
// routeDB may be nil (shared cluster only). appName identifies the caller
// in the customer's pg_stat_activity and connection budget.
func NewResolver(base *pgx.ConnConfig, secret []byte, routeDB dbprovider.TenantDBQuerier, appName string) *Resolver {
	return &Resolver{base: base, secret: secret, routeDB: routeDB, appName: appName, connTimeout: 10 * time.Second}
}

// Configured reports whether a connection can be opened at all.
func (r *Resolver) Configured() bool {
	return r != nil && r.base != nil && len(r.secret) > 0
}

// ConnConfig builds the `<schema>_func` connection config for projectID's
// tenant database: the shared cluster, or a Team project's dedicated
// instance with its per-instance password. Never falls back to shared for
// a Team project whose database isn't serving (ErrNotReady).
func (r *Resolver) ConnConfig(ctx context.Context, projectID, schemaName string) (*pgx.ConnConfig, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}
	role := tenantlogin.FuncRole(schemaName)
	var db dbprovider.TenantDB
	if r.routeDB != nil {
		var err error
		db, err = dbprovider.ResolveTenantDB(ctx, r.routeDB, projectID)
		if errors.Is(err, dbprovider.ErrTenantDBNotReady) {
			return nil, ErrNotReady
		}
		if err != nil {
			slog.Error("tenantconn: tenant database lookup", "project_id", projectID, "error", err)
			return nil, ErrRouting
		}
	}
	if !db.Dedicated {
		cfg := r.base.Copy()
		cfg.User = role
		cfg.Password = tenantlogin.FuncPassword(r.secret, schemaName)
		cfg.RuntimeParams["application_name"] = r.appName
		return cfg, nil
	}
	pw := tenantlogin.FuncPassword(r.secret, tenantlogin.DedicatedSubject(db.ID, schemaName))
	cfg, err := pgx.ParseConfig(dbprovider.BuildOwnerDSN(role, pw, db.Host, db.Port, db.Database))
	if err != nil {
		slog.Error("tenantconn: dedicated database config", "project_id", projectID, "error", err)
		return nil, ErrConnect
	}
	cfg.ConnectTimeout = r.connTimeout
	cfg.RuntimeParams["application_name"] = r.appName
	return cfg, nil
}

// RunInTx opens a short-lived `<schema>_func` connection for projectID,
// begins a transaction with txOpts (e.g. `pgx.TxOptions{AccessMode:
// pgx.ReadOnly}` for the SDK read path; the zero value is read-write,
// default isolation), pins string-literal parsing (parser-agreement with
// the SQL guards), runs setup (the caller's session settings: search_path,
// statement_timeout, RLS identity), then fn, and commits. setup may be nil.
// The connection is closed on return. budget bounds the whole operation,
// routing lookup included (default 45s).
//
// A connect failure returns ErrConnect (the driver error, which names the
// role and internal host, is logged, not returned).
func (r *Resolver) RunInTx(ctx context.Context, projectID, schemaName string, budget time.Duration, txOpts pgx.TxOptions, setup, fn func(context.Context, pgx.Tx) error) error {
	if budget <= 0 {
		budget = 45 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	cfg, err := r.ConnConfig(cctx, projectID, schemaName)
	if err != nil {
		return err
	}
	conn, err := pgx.ConnectConfig(cctx, cfg)
	if err != nil {
		slog.Error("tenantconn: connect as tenant role", "schema", schemaName, "error", err)
		return ErrConnect
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		conn.Close(closeCtx) //nolint:errcheck
	}()

	tx, err := conn.BeginTx(cctx, txOpts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(cctx) //nolint:errcheck

	// Tokenize string literals the way the validators do, whatever the
	// tenant role's defaults say (transaction-local). Every customer-SQL
	// path needs this — hence it lives here, not in each caller.
	if _, err := tx.Exec(cctx, "SET LOCAL standard_conforming_strings = on"); err != nil {
		return fmt.Errorf("pin string parsing: %w", err)
	}
	if setup != nil {
		if err := setup(cctx, tx); err != nil {
			return err
		}
	}
	if err := fn(cctx, tx); err != nil {
		return err
	}
	return tx.Commit(cctx)
}
