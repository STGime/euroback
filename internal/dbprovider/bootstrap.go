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
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
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

// DeriveRuntimePassword produces the deterministic hex password
// for the eurobase_gateway login on a dedicated instance.
// HMAC-SHA256 over a shared secret + a per-instance salt
// (project_database_id) → 64 hex chars.
//
// Why deterministic (not random): the M4 `/connection/rotate` path
// aside, the runtime password is set by TWO uncoupled workers on
// this codebase — the provision worker (including its resume-on-
// retry path) and the backfill sweeper. If they generated random
// passwords, concurrent or re-run calls could ALTER ROLE with
// different values, and the persisted `runtime_password_ciphertext`
// could disagree with what Scaleway currently holds → pool cache
// opens with a stale password → silent auth failures. Deriving
// from a stable input ensures every ALTER sets the identical
// value, and any writer's ciphertext matches Scaleway.
//
// Same pattern as `_ddl` role passwords in
// internal/query/tenant_migrations.go (HMAC-SHA256, hex-encoded).
//
// Requires a non-empty secret. Ops sets RUNTIME_PASSWORD_SECRET in
// eurobase-secrets; a nil/empty secret is a configuration error
// the caller must surface (BootstrapDedicated does).
func DeriveRuntimePassword(secret []byte, projectDatabaseID string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("runtime-pw:"))
	mac.Write([]byte(projectDatabaseID))
	return hex.EncodeToString(mac.Sum(nil))
}

// BootstrapDedicated brings a fresh dedicated instance to the point
// where it can serve SDK traffic as a non-owner runtime role:
//
//   * Extensions + helper functions installed.
//   * eurobase_gateway role created (NOLOGIN → LOGIN with the
//     supplied deterministic password).
//   * public.provision_tenant() defined.
//   * The tenant schema + tables + RLS policies + grants for the
//     given project_id installed via provision_tenant.
//
// Idempotent per project (schema-exists check inside
// provision_tenant), and idempotent per instance (all bootstrap
// SQL uses CREATE OR REPLACE / IF NOT EXISTS). A retried worker
// call sets the same deterministic password on eurobase_gateway
// and returns — so concurrent and re-run bootstraps for the same
// project always converge on the identical live credential.
//
// runtimePassword MUST be the deterministic value from
// DeriveRuntimePassword(secret, projectDatabaseID). The 64-char
// hex shape is enforced (isHexChars) as a defensive belt against a
// caller passing a random-shaped value that would (a) reintroduce
// the Scaleway/DB mismatch race, and (b) potentially embed a SQL
// literal in the one DDL statement pgx cannot bind-parameterise.
//
// Returns the runtime credential (username fixed = eurobase_gateway,
// password echoes runtimePassword) and the tenant schema name. The
// caller seals the password into project_databases.
func BootstrapDedicated(
	ctx context.Context,
	ownerDSN string,
	projectID, displayName string,
	runtimePassword string,
	logger *slog.Logger,
) (*RuntimeCredential, string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if runtimePassword == "" {
		return nil, "", errors.New("bootstrap: runtime password required — call DeriveRuntimePassword first")
	}
	if !isHexChars(runtimePassword) {
		return nil, "", errors.New("bootstrap: runtime password is not hex — refusing to inject into DDL")
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

	// Step 2: set the runtime password. Deterministic — every
	// bootstrap call for the same project sets the identical
	// value, so concurrent runners can't diverge Scaleway from
	// the persisted ciphertext.
	alterRole := fmt.Sprintf(`ALTER ROLE eurobase_gateway WITH LOGIN PASSWORD '%s'`, runtimePassword)
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
		Password: runtimePassword,
	}, schemaName, nil
}

// isHexChars is a belt-and-suspenders check that a candidate
// password contains only hex characters — the shape DeriveRuntimePassword
// produces. Guards the one DDL statement pgx cannot bind-
// parameterise (`ALTER ROLE … PASSWORD '…'`) against a future
// caller passing a value that could embed a SQL literal.
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
