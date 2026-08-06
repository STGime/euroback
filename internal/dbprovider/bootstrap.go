package dbprovider

// Team-tier M2.5 part 2b — bootstrap a fresh dedicated managed-PG
// instance for tenant SDK traffic.
//
// Flow, driven by ProvisionTeamDatabaseWorker after the provider
// reports state=active:
//
//   1. Open a short-lived connection to the dedicated instance as
//      the Scaleway-provisioned owner (eurobase_owner).
//   2. Apply dedicated_bootstrap.sql (extensions, public helpers,
//      NOLOGIN eurobase_gateway role, provision_tenant function).
//      Idempotent: safe to re-run on worker retry.
//   3. Set a fresh LOGIN password on eurobase_gateway and return
//      the plaintext to the caller so it can be sealed + stored.
//      Password never lands in the SQL bundle (which is source-tree
//      committed) and is never logged.
//   4. Call provision_tenant(project_id, name) to create the tenant
//      schema, tables, RLS policies, and grants.
//
// The runtime credential the caller receives here is what M2.5
// part 2a's project_databases.runtime_* columns hold. Once
// populated, PoolCache.Get picks it up automatically (via
// Record.EffectiveCredential) — SDK traffic then lands on the
// dedicated instance as a NON-OWNER, and RLS is enforced.

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed dedicated_bootstrap.sql
var dedicatedBootstrapSQL string

// RuntimeCredential is the non-owner login the SDK pool cache
// authenticates as after bootstrap. Caller seals the Password
// through Cipher.Seal and writes ciphertext + nonce + version into
// project_databases.runtime_*. Plaintext is discarded immediately
// after sealing.
type RuntimeCredential struct {
	Username string
	Password string
}

// BootstrapDedicated brings a fresh dedicated instance to the point
// where it can serve SDK traffic as a non-owner runtime role:
//
//   * Extensions + helper functions installed.
//   * eurobase_gateway role created (NOLOGIN → LOGIN with a fresh
//     32-byte hex password).
//   * public.provision_tenant() defined.
//   * The tenant schema + tables + RLS policies + grants for the
//     given project_id installed via provision_tenant.
//
// Idempotent per project (schema-exists check inside
// provision_tenant), and idempotent per instance (all bootstrap
// SQL uses CREATE OR REPLACE / IF NOT EXISTS). A retried worker
// call will do the minimum work — rotate the runtime password and
// return.
//
// Returns the fresh runtime credential and the tenant schema name.
// The caller is responsible for sealing the password into
// project_databases.
func BootstrapDedicated(
	ctx context.Context,
	ownerDSN string,
	projectID, displayName string,
	logger *slog.Logger,
) (*RuntimeCredential, string, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Short-lived owner connection. We don't cache this — a fresh
	// managed-PG instance is provisioned once and never bootstrapped
	// twice on the happy path. Retries pay the reconnect cost but
	// gain a clean-slate connection.
	connCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cfg, err := pgx.ParseConfig(ownerDSN)
	if err != nil {
		return nil, "", fmt.Errorf("bootstrap: parse owner DSN: %w", err)
	}
	conn, err := pgx.ConnectConfig(connCtx, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("bootstrap: connect as owner: %w", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck

	// Step 1: apply the shared bootstrap SQL. Idempotent — all
	// statements are CREATE ... IF NOT EXISTS, CREATE OR REPLACE
	// FUNCTION, or wrapped in DO $$ ... IF NOT EXISTS blocks.
	if _, err := conn.Exec(ctx, dedicatedBootstrapSQL); err != nil {
		return nil, "", fmt.Errorf("bootstrap: apply bootstrap.sql: %w", err)
	}
	logger.Info("dedicated bootstrap SQL applied")

	// Step 2: rotate the runtime password. Runs on every bootstrap
	// call — a retry that re-enters this path gets a fresh secret,
	// which is safer than reusing whatever might already be on the
	// role. The caller re-seals it into project_databases every
	// time.
	password, err := randomHexPassword32()
	if err != nil {
		return nil, "", fmt.Errorf("bootstrap: generate runtime password: %w", err)
	}
	// The password is passed as a literal in the ALTER ROLE
	// statement. It's 64 hex chars (0-9a-f) so there is nothing
	// to escape, but we still quote-literal it to keep the surface
	// consistent with pgx-parameterised statements elsewhere.
	// pgx does NOT support bind parameters in DDL, so this hand
	// composition is unavoidable. Sanity-check the shape defensively.
	if !isHexChars(password) {
		return nil, "", fmt.Errorf("bootstrap: runtime password is not hex — refusing to inject into DDL")
	}
	alterRole := fmt.Sprintf(`ALTER ROLE eurobase_gateway WITH LOGIN PASSWORD '%s'`, password)
	if _, err := conn.Exec(ctx, alterRole); err != nil {
		return nil, "", fmt.Errorf("bootstrap: set eurobase_gateway password: %w", err)
	}

	// Step 3: provision the tenant schema. This is a call to the
	// function defined by step 1's SQL. Idempotent: if the schema
	// already exists (retry), provision_tenant returns the schema
	// name and exits early — see the pg_namespace guard.
	var schemaName string
	if err := conn.QueryRow(ctx,
		`SELECT public.provision_tenant($1::uuid, $2)`,
		projectID, displayName,
	).Scan(&schemaName); err != nil {
		return nil, "", fmt.Errorf("bootstrap: provision_tenant: %w", err)
	}
	logger.Info("tenant schema provisioned on dedicated instance",
		"project_id", projectID,
		"schema", schemaName)

	return &RuntimeCredential{
		Username: "eurobase_gateway",
		Password: password,
	}, schemaName, nil
}

// randomHexPassword32 returns a 32-byte CSPRNG value hex-encoded to
// 64 characters. Matches the shape used by connection_handlers.go's
// owner rotate path.
func randomHexPassword32() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// isHexChars is a belt-and-suspenders check that a candidate
// password contains only hex characters — the shape randomHexPassword32
// produces. Prevents an accidental future change to the password
// generator from silently opening a SQL-literal injection surface.
func isHexChars(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
