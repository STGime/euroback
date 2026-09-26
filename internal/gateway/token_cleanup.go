package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	edb "github.com/eurobase/euroback/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tokenCleanupInterval controls how often the cleanup job runs.
const tokenCleanupInterval = 1 * time.Hour

// tokenCleanupPerProjectTimeout bounds one project's cleanup.
const tokenCleanupPerProjectTimeout = time.Minute

// tokenCleanupGracePeriod is how long past expires_at a token is kept before
// deletion. Gives us a buffer for clock skew, debugging, and late refresh
// attempts that might benefit from a clear "expired" response instead of
// "not found".
const tokenCleanupGracePeriod = 7 * 24 * time.Hour // 7 days

// DedicatedOpener opens a short-lived pool to a Team project's dedicated
// database (the caller closes it), or returns (nil, nil) when the project
// has none. Wired from dbprovider.OpenOwnerPool in cmd/gateway.
type DedicatedOpener func(ctx context.Context, projectID string) (*pgxpool.Pool, error)

// StartTokenCleanup starts a background goroutine that deletes expired auth
// tokens across every tenant schema and the platform schema.
//
// Targets:
//   - {tenant}.refresh_tokens where expires_at is older than the grace period
//   - {tenant}.email_tokens where expires_at is older than the grace period
//   - public.platform_email_tokens where expires_at is older than the grace period
//
// A Team project's tenant schema lives on its dedicated database (#681):
// its tokens are cleaned there through dedicated (as the owner, a
// short-lived pool per run) — never on the shared cluster, which has no
// schema for a project created as Team and a stale copy for one upgraded
// from Pro. A dedicated database that can't be opened (not active yet,
// no cipher) is skipped until the next run.
//
// Runs once on startup, then every hour. Errors are logged but do not abort
// the loop; individual schema failures are isolated so one broken tenant
// doesn't block cleanup for the others.
func StartTokenCleanup(ctx context.Context, pool *pgxpool.Pool, dedicated DedicatedOpener) {
	go func() {
		ticker := time.NewTicker(tokenCleanupInterval)
		defer ticker.Stop()

		// Run once on startup.
		cleanupExpiredTokens(ctx, pool, dedicated)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupExpiredTokens(ctx, pool, dedicated)
			}
		}
	}()
}

type tokenCleanupTarget struct {
	projectID, schema string
	dedicated         bool
}

// cleanupExpiredTokens deletes expired refresh/email tokens across all tenants
// and the platform. Called by StartTokenCleanup; exposed for testing.
func cleanupExpiredTokens(ctx context.Context, pool *pgxpool.Pool, dedicated DedicatedOpener) {
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

	// Per-tenant cleanup: every active project, with whether it has a live
	// dedicated database (same live-row rule as PlatformTenantContext).
	// NOTE for the Pro→Team data copy: the row turns active before the
	// copy runs; if deleting expired tokens on the target mid-copy ever
	// matters (row-count verification), skip projects with an in-flight
	// project_upgrades row (developer pool — 000117 revokes it from the
	// gateway), as the export does (refuseDuringUpgrade).
	rows, err := pool.Query(ctx,
		`SELECT p.id, p.schema_name,
		        EXISTS (SELECT 1 FROM public.project_databases pd
		                 WHERE pd.project_id = p.id
		                   AND pd.state IN ('provisioning', 'active', 'restoring')
		                   AND pd.deleted_at IS NULL)
		   FROM public.projects p
		  WHERE p.schema_name IS NOT NULL AND p.status = 'active'`,
	)
	if err != nil {
		slog.Error("token cleanup: failed to list project schemas", "error", err)
		return
	}
	var targets []tokenCleanupTarget
	for rows.Next() {
		var t tokenCleanupTarget
		if err := rows.Scan(&t.projectID, &t.schema, &t.dedicated); err != nil {
			slog.Error("token cleanup: scan project row failed", "error", err)
			continue
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("token cleanup: iterate project rows failed", "error", err)
		return
	}

	var totalRefresh, totalEmail int64
	skipped := 0
	for _, t := range targets {
		target := pool
		if t.dedicated {
			var p *pgxpool.Pool
			var err error
			if dedicated != nil {
				p, err = dedicated(ctx, t.projectID)
			}
			if p == nil {
				// Not active yet, no cipher, or gone since the listing:
				// skip — never clean the shared cluster's copy instead.
				slog.Warn("token cleanup: skipping Team project, dedicated database unavailable",
					"project_id", t.projectID, "error", err)
				skipped++
				continue
			}
			target = p
		}
		// Bounded per project: an unreachable dedicated host or a lock
		// wait must not stall the rest of the pass.
		tctx, cancel := context.WithTimeout(ctx, tokenCleanupPerProjectTimeout)
		r, e := cleanupTenantTokens(tctx, target, t.schema, cutoff)
		cancel()
		if t.dedicated {
			target.Close()
		}
		totalRefresh += r
		totalEmail += e
	}

	if totalRefresh > 0 || totalEmail > 0 || skipped > 0 {
		slog.Info("token cleanup: tenant tokens",
			"refresh_tokens_deleted", totalRefresh,
			"email_tokens_deleted", totalEmail,
			"schemas_scanned", len(targets)-skipped,
			"team_projects_skipped", skipped,
		)
	}
}

// cleanupTenantTokens deletes one schema's expired tokens. Each table in
// its own transaction, so a failure on one (it would abort a shared tx)
// doesn't skip the other.
func cleanupTenantTokens(ctx context.Context, pool *pgxpool.Pool, schema string, cutoff time.Time) (refresh, email int64) {
	for _, tbl := range []struct {
		name string
		n    *int64
	}{{"refresh_tokens", &refresh}, {"email_tokens", &email}} {
		// Closes #164. Migration 000055 narrowed the RLS policies on
		// refresh_tokens + email_tokens to require the
		// internal_auth_path intent GUC. RunAsAuthService sets it; a
		// plain pool.Exec would now be RLS-filtered to zero rows. (On a
		// dedicated database the owner bypasses RLS anyway.)
		q := fmt.Sprintf(`DELETE FROM %s WHERE expires_at < $1`, pgx.Identifier{schema, tbl.name}.Sanitize())
		if err := edb.RunAsAuthService(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
			result, err := tx.Exec(ctx, q, cutoff)
			if err == nil {
				*tbl.n = result.RowsAffected()
			}
			return err
		}); err != nil {
			slog.Error("token cleanup: delete failed", "schema", schema, "table", tbl.name, "error", err)
		}
	}
	return refresh, email
}
