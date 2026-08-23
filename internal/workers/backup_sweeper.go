package workers

// Team-tier M3 backup sweepers.
//
// Two responsibilities on one 6-hour ticker:
//   1. Sync backup_snapshots cache from Provider.ListSnapshots for
//      every project with a live project_databases row. Fresh
//      Scaleway automatic backups get discovered; deleted-provider-
//      side ones get expunged from our cache.
//   2. Prune expired rows from backup_snapshots (past expires_at).
//
// Not a River job — same reasoning as StartAuditRetention: this is
// fixed-cadence housekeeping with no upstream trigger.
//
// Related but separate: superseded project_databases rows past
// their 7-day rollback window are torn down by StartDeprovisionSweeper
// (deprovision_sweeper.go), NOT here. This sweeper only touches
// backup_snapshots + the provider snapshot list.

import (
	"context"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/jackc/pgx/v5/pgxpool"
)

const backupSweeperInterval = 6 * time.Hour

// StartBackupSweeper launches the sweeper loop. Returns immediately;
// the loop exits when ctx is cancelled.
func StartBackupSweeper(ctx context.Context, pool *pgxpool.Pool, registry *dbprovider.Registry) {
	go func() {
		// Run once immediately at startup so a freshly-rolled worker
		// pod doesn't wait 6h before its first sync.
		if err := runBackupSweeper(ctx, pool, registry); err != nil {
			slog.Error("backup sweeper: initial run failed", "error", err)
		}
		t := time.NewTicker(backupSweeperInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runBackupSweeper(ctx, pool, registry); err != nil {
					slog.Error("backup sweeper: run failed", "error", err)
				}
			}
		}
	}()
	slog.Info("backup sweeper started", "interval", backupSweeperInterval)
}

func runBackupSweeper(ctx context.Context, pool *pgxpool.Pool, registry *dbprovider.Registry) error {
	// 1. Prune expired.
	tag, err := pool.Exec(ctx,
		`DELETE FROM public.backup_snapshots WHERE expires_at < now()`,
	)
	if err != nil {
		slog.Error("backup sweeper: prune expired failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("backup sweeper: pruned expired snapshots", "count", tag.RowsAffected())
	}

	// 2. Sync live-instance snapshots.
	rows, err := pool.Query(ctx,
		`SELECT project_id::text, id::text, provider, provider_instance_id
		   FROM public.project_databases
		  WHERE state = 'active' AND deleted_at IS NULL`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	type liveDB struct {
		projectID, projectDatabaseID, provider, providerInstanceID string
	}
	var candidates []liveDB
	for rows.Next() {
		var db liveDB
		if err := rows.Scan(&db.projectID, &db.projectDatabaseID, &db.provider, &db.providerInstanceID); err != nil {
			return err
		}
		candidates = append(candidates, db)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, db := range candidates {
		provider, err := registry.Get(db.provider)
		if err != nil {
			slog.Warn("backup sweeper: unknown provider", "provider", db.provider, "project_id", db.projectID)
			continue
		}
		snaps, err := provider.ListSnapshots(ctx, db.providerInstanceID)
		if err != nil {
			slog.Warn("backup sweeper: list snapshots failed",
				"error", err, "project_id", db.projectID, "instance_id", db.providerInstanceID)
			continue
		}
		for _, s := range snaps {
			// UPSERT — the unique index on (project_id, provider_snapshot_id)
			// makes ON CONFLICT deterministic. Provider fields are the
			// source of truth; local metadata (id, synced_at) stays put.
			if _, err := pool.Exec(ctx,
				`INSERT INTO public.backup_snapshots
				    (project_id, project_database_id, provider_snapshot_id,
				     name, size_mb, kind, created_at, expires_at)
				 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)
				 ON CONFLICT (project_id, provider_snapshot_id) DO UPDATE
				    SET name       = EXCLUDED.name,
				        size_mb    = EXCLUDED.size_mb,
				        expires_at = EXCLUDED.expires_at,
				        synced_at  = now()`,
				db.projectID, db.projectDatabaseID, s.ProviderID,
				s.Name, s.SizeMB, string(s.Kind), s.CreatedAt, s.ExpiresAt,
			); err != nil {
				slog.Warn("backup sweeper: upsert failed",
					"error", err, "project_id", db.projectID, "snapshot_id", s.ProviderID)
			}
		}
	}
	return nil
}
