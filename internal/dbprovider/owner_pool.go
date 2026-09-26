package dbprovider

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrDedicatedNotReady is returned by OpenOwnerPool when the project has
// a live dedicated instance that isn't active yet (provisioning or
// restoring, or no host assigned). Its data isn't final, so a job that
// must read all of it (an export) should fail and retry later rather
// than read a half-built or half-restored database.
var ErrDedicatedNotReady = errors.New("dbprovider: the project's dedicated database is not active yet")

// OpenOwnerPool opens a short-lived pool as the OWNER of projectID's live
// dedicated instance, for platform jobs that must read every row of the
// tenant's tables regardless of their RLS policies (compliance exports,
// #663). The owner bypasses RLS on the tables it owns, as
// eurobase_developer does on the shared cluster (#654).
//
// "Live" is the same rule live traffic routes by (GetLiveByProject;
// tenant.PlatformTenantContext): a project_databases row in state
// provisioning / active / restoring. Returns:
//   - pgx.ErrNoRows when the project has none (Free / Pro): the caller
//     reads the shared cluster;
//   - ErrDedicatedNotReady when the live row isn't active or has no host;
//   - an error when the credential can't be opened (a nil cipher
//     included) — never a silent fallback to the shared cluster, where a
//     Team project's schema is missing or stale.
//
// The caller closes the pool. It never goes through PoolCache: the job is
// rare and one-off, so nothing should keep an owner pool open after it.
func OpenOwnerPool(ctx context.Context, repo *Repo, cipher *Cipher, projectID string) (*pgxpool.Pool, error) {
	rec, err := repo.GetLiveByProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("dedicated database lookup: %w", err)
	}
	if rec.State != StateActive || rec.Host == "" || rec.Port == 0 {
		return nil, fmt.Errorf("%w (state %s)", ErrDedicatedNotReady, rec.State)
	}
	if cipher == nil {
		return nil, errors.New("dbprovider: the project has a dedicated database but no connection cipher is configured (VAULT_ENCRYPTION_KEY)")
	}
	password, err := cipher.Open(rec.PasswordCiphertext, rec.PasswordNonce, rec.PasswordKeyVersion)
	if err != nil {
		return nil, fmt.Errorf("dedicated database credential: %w", err)
	}
	cfg, err := pgxpool.ParseConfig(buildDSN(rec.Username, password, rec.Host, rec.Port, rec.DatabaseName))
	if err != nil {
		return nil, fmt.Errorf("dedicated database dsn: %w", err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open dedicated database: %w", err)
	}
	return pool, nil
}
