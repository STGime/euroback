package workers

import (
	"context"
	"errors"

	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/eurobase/euroback/internal/query"
	"github.com/jackc/pgx/v5"
)

// TenantVaultResolver returns a context whose vault reads reach the
// project's own vault — a Team project's dedicated database through a
// short-lived owner pool (never PoolCache, like #681), the shared cluster
// otherwise — and a release func for the pool. An error (dedicated
// database not active, no cipher) fails the job so River retries; never
// the shared cluster's copy (#689).
type TenantVaultResolver func(ctx context.Context, projectID string) (context.Context, func(), error)

// NewTenantVaultResolver builds a TenantVaultResolver from the platform's
// project_databases repo and connection cipher.
func NewTenantVaultResolver(repo *dbprovider.Repo, cipher *dbprovider.Cipher) TenantVaultResolver {
	return func(ctx context.Context, projectID string) (context.Context, func(), error) {
		rec, err := repo.GetLiveByProject(ctx, projectID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ctx, func() {}, nil // shared cluster
		}
		if err != nil {
			return nil, nil, err
		}
		if cipher == nil {
			return nil, nil, errors.New("the project has a dedicated database but no connection cipher is configured (VAULT_ENCRYPTION_KEY)")
		}
		pool, err := dbprovider.OpenOwnerPoolFor(ctx, rec, cipher)
		if err != nil {
			return nil, nil, err
		}
		return query.WithDedicatedDB(query.ContextWithTenantPool(ctx, pool)), pool.Close, nil
	}
}
