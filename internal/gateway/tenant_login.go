package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/tenantconn"
	"github.com/jackc/pgx/v5"
)

// sdkTenantLogin adapts a tenantconn.Resolver to query.TenantLoginRunner so
// SDK customer SQL (/v1/db/sql and RPC) runs on the project's own
// `<schema>_func` role instead of the shared gateway pool — the step-4
// security remediation (customer SQL must never share a platform login).
//
// It reads the request's ProjectContext (already loaded by the API-key
// middleware) to decide routing without an extra per-request lookup:
//   - no dedicated DB (Free/Pro) → the shared cluster via the gateway
//     PgBouncer pooler, no routing query (RunInTxShared);
//   - HasDedicatedDB (Team) → the project's dedicated instance as
//     `<schema>_func` (RunInTx resolves the live row itself).
//
// A login that can't be opened (not ready / routing / connect) is mapped to
// query.ErrTenantLoginUnavailable so handlers answer 503 — never a
// fall-through to the shared cluster or a shared platform login.
type sdkTenantLogin struct {
	resolver *tenantconn.Resolver
}

func (s sdkTenantLogin) Run(ctx context.Context, schema string, readOnly bool, setup, fn func(context.Context, pgx.Tx) error) error {
	txOpts := pgx.TxOptions{}
	if readOnly {
		txOpts.AccessMode = pgx.ReadOnly
	}

	var err error
	if pc, ok := auth.ProjectFromContext(ctx); ok && pc != nil && pc.HasDedicatedDB {
		// Team project: route to its dedicated instance (the resolver
		// resolves the live project_databases row and refuses — never
		// shared — if it isn't serving).
		//
		// PRE-FLIP BLOCKER FOR TEAM (not the shared-cluster Free/Pro path):
		// this opens a direct, un-pooled `<schema>_func` connection to the
		// dedicated instance per request (tenantconn bypasses the gateway
		// PgBouncer for dedicated hosts). It competes with the functions
		// runner and cron for that instance's DedicatedFuncConnLimit (12),
		// and every request pays a TLS+SCRAM connect. Before enabling
		// SDK_FUNC_LOGIN for Team projects, add a pooled/capped dedicated
		// path (see the dedicated-pooler work, #485) or validate the burst
		// budget. The shared-cluster path below is pooled via pgbouncer.
		err = s.resolver.RunInTx(ctx, pc.ProjectID, schema, 0, txOpts, setup, fn)
	} else {
		// Free/Pro on the shared cluster: skip the routing lookup.
		err = s.resolver.RunInTxShared(ctx, schema, 0, txOpts, setup, fn)
	}

	// Connection/routing failures become a tenant-safe 503 signal; the
	// underlying driver detail was already logged by tenantconn.
	switch {
	case errors.Is(err, tenantconn.ErrNotReady),
		errors.Is(err, tenantconn.ErrRouting),
		errors.Is(err, tenantconn.ErrConnect),
		errors.Is(err, tenantconn.ErrNotConfigured):
		return fmt.Errorf("%w: %v", query.ErrTenantLoginUnavailable, err)
	}
	return err
}

// ddlTenantLogin wraps the platform `_ddl` runner (step 6d) so a
// connection/login failure becomes query.ErrTenantLoginUnavailable (→ 503)
// in the console SQL handlers, matching the SDK `_func` path's contract.
type ddlTenantLogin struct {
	inner query.TenantLoginRunner
}

func (d ddlTenantLogin) Run(ctx context.Context, schema string, readOnly bool, setup, fn func(context.Context, pgx.Tx) error) error {
	err := d.inner.Run(ctx, schema, readOnly, setup, fn)
	switch {
	case errors.Is(err, tenantconn.ErrNotReady),
		errors.Is(err, tenantconn.ErrRouting),
		errors.Is(err, tenantconn.ErrConnect),
		errors.Is(err, tenantconn.ErrNotConfigured):
		return fmt.Errorf("%w: %v", query.ErrTenantLoginUnavailable, err)
	}
	return err
}
