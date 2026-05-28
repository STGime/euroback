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

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// RunAsAuthService is RunAsService PLUS an additional GUC
// `app.intent='internal_auth_path'`. Used exclusively by the auth /
// email / SMS / vault code paths that legitimately need to read or
// write the credential and secret tables (`refresh_tokens`,
// `email_tokens`, `vault_secrets`). Closes #164 — the MCP prompt-
// injection mitigation.
//
// Why this exists alongside RunAsService:
// Migration 000055 narrows the RLS policy on those three tables from
// `is_service_role()` to `is_internal_auth_path()`. The generic SQL
// handler (which the MCP server's `runSQL` tool ultimately calls) sets
// `app.end_user_role='service'` but NOT `app.intent`. So a prompt-
// injected SQL query — no matter what the LLM is tricked into
// emitting — cannot satisfy the policy on those tables and gets back
// zero rows.
//
// Only Go code paths that ALWAYS run as part of legitimate auth flows
// (refresh, email-token verify, magic link, vault encrypt/decrypt,
// GDPR export, background token cleanup) get this helper. Adding a
// new caller is a security-review-worthy change.
func RunAsAuthService(ctx context.Context, pool *pgxpool.Pool, fn func(context.Context, pgx.Tx) error) error {
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
	if _, err := tx.Exec(ctx, "SELECT set_config('app.intent', 'internal_auth_path', true)"); err != nil {
		return fmt.Errorf("set internal_auth_path intent: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
