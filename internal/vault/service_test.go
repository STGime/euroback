package vault

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Closes #71 (regression introduced in PR #65). PR #65 tightened the
// vault_secrets RLS policy from `USING (true)` to
// `USING (public.is_service_role())`. The previous service code talked
// directly to the gateway pool with no service-role context — every
// vault operation silently failed RLS. These tests pin the round-trip
// so the regression class can't recur.
//
// Run locally with a real Postgres + applied migrations + the test
// project provisioned by setupTestDB-equivalent. Skips otherwise.

func setupVaultTest(t *testing.T) (*VaultService, string, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}

	// Generate a 32-byte AES key for the test, base64-encoded.
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		pool.Close()
		t.Fatalf("rand.Read: %v", err)
	}
	encKey := base64.StdEncoding.EncodeToString(keyBytes)

	svc, err := NewVaultService(pool, encKey)
	if err != nil {
		pool.Close()
		t.Fatalf("NewVaultService: %v", err)
	}

	// Provision a test tenant. Mirrors the pattern from
	// internal/query/engine_test.go setupTestDB. Reuses the
	// public.platform_users + public.projects + provision_tenant() chain.
	hankoUserID := fmt.Sprintf("test-vault-%d", os.Getpid())
	var ownerID string
	err = pool.QueryRow(ctx,
		`INSERT INTO platform_users (hanko_user_id, email)
		 VALUES ($1, $2)
		 ON CONFLICT (hanko_user_id) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		hankoUserID, "vaulttest@eurobase.app",
	).Scan(&ownerID)
	if err != nil {
		pool.Close()
		t.Skipf("cannot create test platform user: %v", err)
	}

	slug := fmt.Sprintf("test-vault-%d", os.Getpid())
	schemaPlaceholder := fmt.Sprintf("tenant_test_vault_%d", os.Getpid())
	s3Placeholder := fmt.Sprintf("eurobase-test-vault-%d", os.Getpid())
	var projectID string
	err = pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'provisioning')
		 RETURNING id`,
		ownerID, "Vault Test", slug, schemaPlaceholder, s3Placeholder, "fr-par", "free",
	).Scan(&projectID)
	if err != nil {
		pool.Close()
		t.Skipf("cannot create test project: %v", err)
	}

	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1, $2, $3)`, projectID, "Vault Test", "free"); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
		pool.Close()
		t.Skipf("cannot provision tenant: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE projects SET status = 'active' WHERE id = $1`, projectID); err != nil {
		_, _ = pool.Exec(ctx, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
		pool.Close()
		t.Skipf("cannot activate project: %v", err)
	}

	var schemaName string
	if err := pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schemaName); err != nil {
		pool.Close()
		t.Skipf("cannot read schema_name: %v", err)
	}

	cleanup := func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
		pool.Close()
	}
	t.Cleanup(cleanup)
	return svc, schemaName, cleanup
}

// TestVaultRoundTrip_SetGetListDelete is the regression-pin for #71.
// The pre-fix code path would fail at Set with a 500 because RLS denies
// the INSERT — no service-role context.
func TestVaultRoundTrip_SetGetListDelete(t *testing.T) {
	svc, schema, _ := setupVaultTest(t)
	ctx := context.Background()

	// Set
	const name = "TEST_KEY"
	const value = "s3cr3t-v4lu3"
	const desc = "test secret"
	created, err := svc.Set(ctx, schema, name, value, desc)
	if err != nil {
		t.Fatalf("Set failed (would have happened pre-fix; #71): %v", err)
	}
	if created.Name != name || created.Description != desc {
		t.Errorf("Set returned %+v, want name=%q desc=%q", created, name, desc)
	}

	// Get
	got, err := svc.Get(ctx, schema, name)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Value != value {
		t.Errorf("Get returned value %q, want %q", got.Value, value)
	}
	if got.Description != desc {
		t.Errorf("Get returned desc %q, want %q", got.Description, desc)
	}

	// List
	all, err := svc.List(ctx, schema)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List returned %d secrets, want 1", len(all))
	}
	for _, s := range all {
		if s.Value != "" {
			t.Errorf("List should not include decrypted value, got %q", s.Value)
		}
	}

	// Update value + description
	newVal := "new-value"
	newDesc := "updated"
	updated, err := svc.Update(ctx, schema, name, &newVal, &newDesc)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updated.Description != newDesc {
		t.Errorf("Update returned desc %q, want %q", updated.Description, newDesc)
	}
	got2, _ := svc.Get(ctx, schema, name)
	if got2.Value != newVal {
		t.Errorf("Get after Update returned %q, want %q", got2.Value, newVal)
	}

	// Count
	count, err := svc.Count(ctx, schema)
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Count = %d, want 1", count)
	}

	// HasRaw
	has, err := svc.HasRaw(ctx, schema, name)
	if err != nil || !has {
		t.Errorf("HasRaw(existing) = %v, %v; want true, nil", has, err)
	}
	hasMissing, err := svc.HasRaw(ctx, schema, "DOES_NOT_EXIST")
	if err != nil || hasMissing {
		t.Errorf("HasRaw(missing) = %v, %v; want false, nil", hasMissing, err)
	}

	// Delete
	if err := svc.Delete(ctx, schema, name); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	count2, _ := svc.Count(ctx, schema)
	if count2 != 0 {
		t.Errorf("Count after Delete = %d, want 0", count2)
	}

	// Get after delete should error not-found.
	_, err = svc.Get(ctx, schema, name)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Get after Delete: expected not-found error, got %v", err)
	}
}

// TestVaultRawHelpers_AreServiceScoped covers the SetRaw/GetRaw/HasRaw
// path used by tenant-side OAuth-secret storage. Same regression class
// as #71 — these wrap Set/Get and inherit the service-role behaviour.
func TestVaultRawHelpers_AreServiceScoped(t *testing.T) {
	svc, schema, _ := setupVaultTest(t)
	ctx := context.Background()

	const name = "OAUTH_GOOGLE_SECRET"
	const value = "oauth-client-secret-xyz"

	if err := svc.SetRaw(ctx, schema, name, value); err != nil {
		t.Fatalf("SetRaw failed (would have happened pre-fix; #71): %v", err)
	}
	got, err := svc.GetRaw(ctx, schema, name)
	if err != nil {
		t.Fatalf("GetRaw failed: %v", err)
	}
	if got != value {
		t.Errorf("GetRaw = %q, want %q", got, value)
	}

	// GetRaw on missing returns "" with nil error (idempotency for
	// optional secrets).
	missing, err := svc.GetRaw(ctx, schema, "NOT_PRESENT")
	if err != nil {
		t.Errorf("GetRaw(missing) returned error: %v", err)
	}
	if missing != "" {
		t.Errorf("GetRaw(missing) = %q, want empty", missing)
	}

	// DeleteRaw is idempotent.
	if err := svc.DeleteRaw(ctx, schema, "NEVER_EXISTED"); err != nil {
		t.Errorf("DeleteRaw(missing) returned error: %v", err)
	}
	if err := svc.DeleteRaw(ctx, schema, name); err != nil {
		t.Errorf("DeleteRaw(existing) returned error: %v", err)
	}
}

// TestVaultRekeySchema_LegacyToCurrent exercises the full rotation path
// against a real database: a row sealed at the legacy version 0 (shared
// master key, simulating a pre-#167 secret) is re-encrypted under the
// current per-tenant version, while a row already at the current version is
// left untouched. This covers the select-FOR UPDATE → decrypt-at-old-version
// → re-seal-at-current → update transaction in RekeySchema — the rotation
// path GDPR review depends on.
func TestVaultRekeySchema_LegacyToCurrent(t *testing.T) {
	svc, schema, _ := setupVaultTest(t)
	ctx := context.Background()

	hk, ok := svc.provider.(*hkdfKeyProvider)
	if !ok {
		t.Fatalf("expected *hkdfKeyProvider, got %T", svc.provider)
	}

	// Seal one secret at the legacy version 0 (shared master key) by
	// temporarily pointing the provider's current version at 0, then
	// restore it so subsequent writes use the per-tenant v1.
	hk.current = legacyKeyVersion
	if _, err := svc.Set(ctx, schema, "LEGACY", "v0-value", "from before per-tenant keys"); err != nil {
		t.Fatalf("Set legacy secret: %v", err)
	}
	hk.current = firstDerivedKeyVersion

	// A fresh secret is sealed at the current per-tenant version.
	if _, err := svc.Set(ctx, schema, "MODERN", "v1-value", ""); err != nil {
		t.Fatalf("Set modern secret: %v", err)
	}

	// First rekey: only the legacy (v0) row is below the current version,
	// so exactly one row should be re-encrypted.
	n, err := svc.RekeySchema(ctx, schema)
	if err != nil {
		t.Fatalf("RekeySchema: %v", err)
	}
	if n != 1 {
		t.Errorf("first RekeySchema rekeyed %d rows, want 1 (only the v0 row)", n)
	}

	// Both secrets must still decrypt to their original plaintext after the
	// key change.
	legacy, err := svc.Get(ctx, schema, "LEGACY")
	if err != nil {
		t.Fatalf("Get LEGACY after rekey: %v", err)
	}
	if legacy.Value != "v0-value" {
		t.Errorf("LEGACY value after rekey = %q, want %q", legacy.Value, "v0-value")
	}
	modern, err := svc.Get(ctx, schema, "MODERN")
	if err != nil {
		t.Fatalf("Get MODERN after rekey: %v", err)
	}
	if modern.Value != "v1-value" {
		t.Errorf("MODERN value after rekey = %q, want %q", modern.Value, "v1-value")
	}

	// Second rekey is a no-op: every row is now at the current version.
	n2, err := svc.RekeySchema(ctx, schema)
	if err != nil {
		t.Fatalf("second RekeySchema: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second RekeySchema rekeyed %d rows, want 0 (all already current)", n2)
	}
}
