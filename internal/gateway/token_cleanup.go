package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tokenCleanupInterval controls how often the cleanup job runs.
const tokenCleanupInterval = 1 * time.Hour

// tokenCleanupGracePeriod is how long past expires_at a token is kept before
// deletion. Gives us a buffer for clock skew, debugging, and late refresh
// attempts that might benefit from a clear "expired" response instead of
// "not found".
const tokenCleanupGracePeriod = 7 * 24 * time.Hour // 7 days

// StartTokenCleanup starts a background goroutine that deletes expired auth
// tokens across every tenant schema and the platform schema.
//
// Targets:
//   - {tenant}.refresh_tokens where expires_at is older than the grace period
//   - {tenant}.email_tokens where expires_at is older than the grace period
//   - public.platform_email_tokens where expires_at is older than the grace period
//
// Runs once on startup, then every hour. Errors are logged but do not abort
// the loop; individual schema failures are isolated so one broken tenant
// doesn't block cleanup for the others.
func StartTokenCleanup(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(tokenCleanupInterval)
		defer ticker.Stop()

		// Run once on startup.
		cleanupExpiredTokens(ctx, pool)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupExpiredTokens(ctx, pool)
			}
		}
	}()
}

// cleanupExpiredTokens deletes expired refresh/email tokens across all tenants
// and the platform. Called by StartTokenCleanup; exposed for testing.
func cleanupExpiredTokens(ctx context.Context, pool *pgxpool.Pool) {
	cutoff := time.Now().Add(-tokenCleanupGracePeriod)

	// Platform-level cleanup.
	if result, err := pool.Exec(ctx,
		`DELETE FROM public.platform_email_tokens WHERE expires_at < $1`,
		cutoff,
	); err != nil {
		slog.Error("token cleanup: platform_email_tokens delete failed", "error", err)
	} else if n := result.RowsAffected(); n > 0 {
		slog.Info("token cleanup: platform_email_tokens", "deleted", n)
	}

	// Per-tenant cleanup: walk every active project schema.
	rows, err := pool.Query(ctx,
		`SELECT schema_name FROM public.projects
		 WHERE schema_name IS NOT NULL AND status = 'active'`,
	)
	if err != nil {
		slog.Error("token cleanup: failed to list project schemas", "error", err)
		return
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			slog.Error("token cleanup: scan schema row failed", "error", err)
			continue
		}
		schemas = append(schemas, schema)
	}
	if err := rows.Err(); err != nil {
		slog.Error("token cleanup: iterate project rows failed", "error", err)
		return
	}

	totalRefresh := int64(0)
	totalEmail := int64(0)
	for _, schema := range schemas {
		// refresh_tokens
		if result, err := pool.Exec(ctx,
			fmt.Sprintf(`DELETE FROM %q.refresh_tokens WHERE expires_at < $1`, schema),
			cutoff,
		); err != nil {
			slog.Error("token cleanup: refresh_tokens delete failed", "schema", schema, "error", err)
		} else {
			totalRefresh += result.RowsAffected()
		}

		// email_tokens
		if result, err := pool.Exec(ctx,
			fmt.Sprintf(`DELETE FROM %q.email_tokens WHERE expires_at < $1`, schema),
			cutoff,
		); err != nil {
			slog.Error("token cleanup: email_tokens delete failed", "schema", schema, "error", err)
		} else {
			totalEmail += result.RowsAffected()
		}
	}

	if totalRefresh > 0 || totalEmail > 0 {
		slog.Info("token cleanup: tenant tokens",
			"refresh_tokens_deleted", totalRefresh,
			"email_tokens_deleted", totalEmail,
			"schemas_scanned", len(schemas),
		)
	}
}
