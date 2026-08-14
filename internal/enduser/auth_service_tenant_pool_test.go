package enduser

import (
	"context"
	"testing"

	"github.com/eurobase/euroback/internal/query"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestAuthService_tenantPool pins the two-branch contract mirrored
// from vault.VaultService.tenantPool:
//
//   * ctx has NO stashed tenant pool → fall back to s.pool
//     (Free/Pro path — the middleware doesn't stash for projects
//     without a dedicated instance).
//   * ctx HAS a stashed tenant pool via query.ContextWithTenantPool
//     → use it (Team-tier path — sdkTenantPoolMw stashed the
//     dedicated runtime pool).
//
// Regression guard for #393 / the class of bug PR #391's review
// swept but had to defer the AuthService half of. Without this
// routing, every /v1/auth/** endpoint (signup / signin / refresh /
// magic link / phone / OAuth callback find-or-create) 42P01s on
// shared for Team-tier post-#378 — SDK end-user auth completely
// broken.
//
// No Postgres — pointer identity check only. Runs in the default
// `go test ./...` sweep.
func TestAuthService_tenantPool(t *testing.T) {
	shared := &pgxpool.Pool{}
	dedicated := &pgxpool.Pool{}
	s := &AuthService{pool: shared}

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
		// ContextWithTenantPool short-circuits nil, so this ctx is
		// equivalent to Background(). Belt + suspenders in case
		// someone bypasses the helper.
		ctx := query.ContextWithTenantPool(context.Background(), nil)
		got := s.tenantPool(ctx)
		if got != shared {
			t.Fatalf("expected shared pool on nil stash, got %p", got)
		}
	})
}
