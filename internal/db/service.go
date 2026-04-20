// Package db provides helpers for running queries with explicit database
// role context. Split out so the auth, tenant, storage, and vault
// packages can all reuse the same "service role" transaction pattern
// without a circular dependency.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RunAsService opens a transaction on pool, sets app.end_user_role to
// 'service' for the duration of that transaction, and invokes fn with the
// tx. On success the transaction is committed; on any error it is rolled
// back.
//
// Use this for paths that legitimately have no end-user context:
//
//   - Platform-admin CRUD on tenant tables (list/create/update/delete users).
//   - Pre-auth lookups (sign-in by email, sign-up INSERT, password reset
//     by token, magic-link creation, OAuth user-by-provider lookup,
//     phone-OTP send).
//   - GDPR export on behalf of a user (actor is platform admin, not the
//     exported user).
//   - Email/SMS token INSERT + UPDATE (caller is pre-auth).
//
// Tenant RLS policies recognise app.end_user_role='service' as a bypass
// (see migration 000038). The transaction-local SET ensures the bypass
// never leaks into a subsequent query that legitimately needs RLS.
func RunAsService(ctx context.Context, pool *pgxpool.Pool, fn func(context.Context, pgx.Tx) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.end_user_role', 'service', true)"); err != nil {
		return fmt.Errorf("set service role: %w", err)
	}
	// Also disable RLS for this tx. The service-role GUC is sufficient
	// for the platform-hardcoded policies (rewritten in migration 000038)
	// but not for user-created tables whose policies predate that change
	// and don't branch on public.is_service_role(). `row_security = off`
	// is an authoritative bypass for roles with BYPASSRLS (granted in
	// migration 000039); a no-op otherwise, which is still safe because
	// the service role GUC covers the migrator-owned tables.
	if _, err := tx.Exec(ctx, "SET LOCAL row_security = off"); err != nil {
		return fmt.Errorf("disable row security: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
