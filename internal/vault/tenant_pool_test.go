package vault

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/query"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestVaultService_tenantPool pins the contract (plus: a project with a
// dedicated database but no pool on the request is refused, #678):
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
		got, _ := s.tenantPool(context.Background())
		if got != shared {
			t.Fatalf("expected shared pool, got %p (shared=%p)", got, shared)
		}
	})

	t.Run("tenant pool stashed → tenant pool", func(t *testing.T) {
		ctx := query.ContextWithTenantPool(context.Background(), dedicated)
		got, _ := s.tenantPool(ctx)
		if got != dedicated {
			t.Fatalf("expected dedicated pool, got %p (dedicated=%p, shared=%p)", got, dedicated, shared)
		}
	})

	t.Run("dedicated project without a pool → refused, never shared (#678)", func(t *testing.T) {
		ctx := auth.ContextWithProject(context.Background(), &auth.ProjectContext{ProjectID: "p", HasDedicatedDB: true})
		got, err := s.tenantPool(ctx)
		if !errors.Is(err, ErrDedicatedPoolUnavailable) || got != nil {
			t.Fatalf("got pool %p, err %v; want nil, ErrDedicatedPoolUnavailable", got, err)
		}
	})

	t.Run("dedicated project with its pool → that pool", func(t *testing.T) {
		ctx := auth.ContextWithProject(context.Background(), &auth.ProjectContext{ProjectID: "p", HasDedicatedDB: true})
		ctx = query.ContextWithTenantPool(ctx, dedicated)
		if got, err := s.tenantPool(ctx); err != nil || got != dedicated {
			t.Fatalf("got %p, %v; want dedicated", got, err)
		}
	})

	t.Run("nil tenant pool stashed → shared pool (defensive)", func(t *testing.T) {
		// ContextWithTenantPool short-circuits nil (it's a no-op),
		// so this ctx is equivalent to Background(). Belt +
		// suspenders in case someone bypasses the helper.
		ctx := query.ContextWithTenantPool(context.Background(), nil)
		got, _ := s.tenantPool(ctx)
		if got != shared {
			t.Fatalf("expected shared pool on nil stash, got %p", got)
		}
	})
}

// A Team project's vault is on a database the customer owns: a legacy
// key_version 0 row there (never written by the platform) would make the
// gateway decrypt raw-master-key ciphertext for them — refused. Shared
// rows may still be legacy until rekeyed.
func TestVaultService_legacyRowRefusedOnDedicated(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s, err := NewVaultService(&pgxpool.Pool{}, base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	const schema = "tenant_abc"
	legacyCT, legacyNonce, err := encryptWith(key, "raw-key-sealed")
	if err != nil {
		t.Fatal(err)
	}
	v1CT, v1Nonce, v1, err := s.seal(context.Background(), schema, "per-tenant")
	if err != nil || v1 == legacyKeyVersion {
		t.Fatalf("seal: v%d %v", v1, err)
	}
	shared := context.Background()
	dedicatedCtxs := map[string]context.Context{
		"pool on request":       query.ContextWithTenantPool(context.Background(), &pgxpool.Pool{}),
		"project has dedicated": auth.ContextWithProject(context.Background(), &auth.ProjectContext{ProjectID: "p", HasDedicatedDB: true}),
	}

	if got, err := s.openVaultRow(shared, schema, legacyCT, legacyNonce, legacyKeyVersion); err != nil || got != "raw-key-sealed" {
		t.Fatalf("shared legacy row: %q, %v", got, err)
	}
	for name, ctx := range dedicatedCtxs {
		if _, err := s.openVaultRow(ctx, schema, legacyCT, legacyNonce, legacyKeyVersion); !errors.Is(err, ErrLegacyOnDedicated) {
			t.Fatalf("%s: legacy row err %v, want ErrLegacyOnDedicated", name, err)
		}
		if got, err := s.openVaultRow(ctx, schema, v1CT, v1Nonce, v1); err != nil || got != "per-tenant" {
			t.Fatalf("%s: per-tenant row: %q, %v", name, got, err)
		}
	}
}

// A malformed row (e.g. planted on a dedicated database) is an error, not
// a GCM panic.
func TestVaultService_malformedNonceIsError(t *testing.T) {
	s, err := NewVaultService(&pgxpool.Pool{}, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := query.ContextWithTenantPool(context.Background(), &pgxpool.Pool{})
	if _, err := s.openVaultRow(ctx, "tenant_abc", []byte("x"), []byte("x"), 1); err == nil {
		t.Fatal("malformed nonce opened")
	}
}
