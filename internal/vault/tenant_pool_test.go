package vault

import (
	"context"
	"testing"

	"github.com/eurobase/euroback/internal/query"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestVaultService_tenantPool pins the two-branch contract:
//
//   * ctx has NO stashed tenant pool → fall back to s.pool
//     (Free/Pro path — the middleware doesn't stash for projects
//     without a dedicated instance).
//   * ctx HAS a stashed tenant pool via query.ContextWithTenantPool
//     → use it (Team-tier path — PlatformTenantContext or
//     sdkTenantPoolMw stashed the dedicated pool).
//
// Regression guard for the class of bug PR-C's sweep missed on
// vault (this PR) and #392's sweep missed on token cleanup: a
// future refactor that stops honouring ContextWithTenantPool would
// silently route Team-tier vault traffic back to the shared pool
// where vault_secrets doesn't exist, so every Team-tier vault
// request would 42P01 the way they did before this PR shipped.
//
// No Postgres needed — the two pools are compared by pointer, no
// query ever runs. Fast enough for the default `go test ./...`.
func TestVaultService_tenantPool(t *testing.T) {
	// Zero-value pools are enough for the identity check; nothing
	// touches them (no Ping / no Acquire).
	shared := &pgxpool.Pool{}
	dedicated := &pgxpool.Pool{}
	s := &VaultService{pool: shared}

	t.Run("no tenant pool stashed → shared pool", func(t *testing.T) {
		got := s.tenantPool(context.Background())
		if got != shared {
			t.Fatalf("expected shared pool, got %p (shared=%p)", got, shared)
		}
	})

	t.Run("tenant pool stashed → tenant pool", func(t *testing.T) {
		ctx := query.ContextWithTenantPool(context.Background(), dedicated)
		got := s.tenantPool(ctx)
		if got != dedicated {
			t.Fatalf("expected dedicated pool, got %p (dedicated=%p, shared=%p)", got, dedicated, shared)
		}
	})

	t.Run("nil tenant pool stashed → shared pool (defensive)", func(t *testing.T) {
		// ContextWithTenantPool short-circuits nil (it's a no-op),
		// so this ctx is equivalent to Background(). Belt +
		// suspenders in case someone bypasses the helper.
		ctx := query.ContextWithTenantPool(context.Background(), nil)
		got := s.tenantPool(ctx)
		if got != shared {
			t.Fatalf("expected shared pool on nil stash, got %p", got)
		}
	})
}
