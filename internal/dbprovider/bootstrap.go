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

// DeriveReadonlyPassword mirrors DeriveRuntimePassword for the
// eurobase_readonly login. Same secret input, distinct domain-
// separator tag so the two derived passwords can never collide even
// under identical projectDatabaseID. Same retry/idempotency reasoning:
// deterministic → every ALTER ROLE sets the identical value → the
// persisted ciphertext always matches Scaleway.
func DeriveReadonlyPassword(secret []byte, projectDatabaseID string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("readonly-pw:"))
	mac.Write([]byte(projectDatabaseID))
	return hex.EncodeToString(mac.Sum(nil))
}

// BootstrapCredentials bundles the two non-owner credentials the
// dedicated bootstrap materialises: the runtime cred (read/write, SDK
// traffic) and the readonly cred (SELECT-only, M4 Direct Connection
// UI's "Read-only" toggle). Caller seals both passwords through
// Cipher.Seal and persists each into its own project_databases
// column set (runtime_* / readonly_*).
type BootstrapCredentials struct {
	Runtime  RuntimeCredential
	Readonly RuntimeCredential
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
// Returns both non-owner credentials the caller must seal + persist:
// the runtime cred (eurobase_gateway, R/W) and the readonly cred
// (eurobase_readonly, SELECT-only). Also returns the tenant schema
// name. Both passwords echo their derived inputs; the SQL layer
// materialises them via ALTER ROLE and grants happen in
// provision_tenant.
func BootstrapDedicated(
	ctx context.Context,
	ownerDSN string,
	projectID, displayName string,
	runtimePassword string,
	readonlyPassword string,
	logger *slog.Logger,
) (*BootstrapCredentials, string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if runtimePassword == "" {
		return nil, "", errors.New("bootstrap: runtime password required — call DeriveRuntimePassword first")
	}
	if !isHexChars(runtimePassword) {
		return nil, "", errors.New("bootstrap: runtime password is not hex — refusing to inject into DDL")
	}
	if readonlyPassword == "" {
		return nil, "", errors.New("bootstrap: readonly password required — call DeriveReadonlyPassword first")
	}
	if !isHexChars(readonlyPassword) {
		return nil, "", errors.New("bootstrap: readonly password is not hex — refusing to inject into DDL")
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
	if _, err := conn.Exec(ctx,
		fmt.Sprintf(`ALTER ROLE eurobase_gateway WITH LOGIN PASSWORD '%s'`, runtimePassword),
	); err != nil {
		return nil, "", fmt.Errorf("bootstrap: set eurobase_gateway password: %w", err)
	}

	// Step 2b: set the readonly password. Same deterministic pattern
	// as the runtime password, distinct HMAC domain tag → the two
	// passwords never collide.
	if _, err := conn.Exec(ctx,
		fmt.Sprintf(`ALTER ROLE eurobase_readonly WITH LOGIN PASSWORD '%s'`, readonlyPassword),
	); err != nil {
		return nil, "", fmt.Errorf("bootstrap: set eurobase_readonly password: %w", err)
	}

	// Step 3: provision the tenant schema. This is a call to the
	// function defined by step 1's SQL. Idempotent: if the schema
	// already exists (retry), provision_tenant returns the schema
	// name and exits early — see the pg_namespace guard. Per-tenant
	// grants for BOTH eurobase_gateway and eurobase_readonly live at
	// the end of provision_tenant's body.
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

	return &BootstrapCredentials{
		Runtime: RuntimeCredential{
			Username: "eurobase_gateway",
			Password: runtimePassword,
		},
		Readonly: RuntimeCredential{
			Username: "eurobase_readonly",
			Password: readonlyPassword,
		},
	}, schemaName, nil
}

// LockdownReadonlyGrants forces eurobase_readonly to SELECT-only on
// the tenant schema. Needed because Scaleway RDB's
// PUT /rdb/v1/…/privileges endpoint (called by the worker after
// BootstrapDedicated to get CONNECT) with `permission=readonly` in
// fact grants MORE than SELECT — verified empirically against
// myteam3 today: after the API call, eurobase_readonly could
// INSERT into `tenant_….todos`. Their `readonly` naming is
// misleading; the effective grant looks like CRUD + a matching
// ALTER DEFAULT PRIVILEGES rule so future tables inherit it.
//
// This function REVOKEs the writes and re-asserts SELECT-only via
// both `ON ALL TABLES` (existing) and `ALTER DEFAULT PRIVILEGES`
// (future). Runs as eurobase_owner (the DSN's user), which has
// grantor rights on schemas it owns — no _rdb_superadmin required.
//
// Idempotent: repeat runs converge on the same state.
//
// Called from the worker path AFTER SetPrivilege so it always wins
// against the Scaleway API's implicit grants.
func LockdownReadonlyGrants(ctx context.Context, ownerDSN, schemaName string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if schemaName == "" {
		return errors.New("lockdown readonly: schemaName required")
	}

	connCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cfg, err := pgx.ParseConfig(ownerDSN)
	if err != nil {
		return fmt.Errorf("lockdown readonly: parse owner DSN: %w", err)
	}
	conn, err := pgx.ConnectConfig(connCtx, cfg)
	if err != nil {
		return fmt.Errorf("lockdown readonly: connect as owner: %w", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck

	stmts := []string{
		// Existing tables in the tenant schema — REVOKE all writes.
		fmt.Sprintf(`REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA %q FROM eurobase_readonly`, schemaName),
		// Existing sequences — REVOKE UPDATE (which allows nextval/setval writes);
		// SELECT + USAGE stay so cursor-based reads work.
		fmt.Sprintf(`REVOKE UPDATE ON ALL SEQUENCES IN SCHEMA %q FROM eurobase_readonly`, schemaName),
		// Future tables/sequences: reset default privileges from the
		// Scaleway grant back to SELECT-only. The ALTER DEFAULT
		// PRIVILEGES calls in provision_tenant already set SELECT;
		// this REVOKE strips any extras Scaleway added.
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA %q REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM eurobase_readonly`, schemaName),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA %q REVOKE UPDATE ON SEQUENCES FROM eurobase_readonly`, schemaName),
		// Re-assert SELECT to be safe (idempotent; already there from
		// provision_tenant but explicit is better in a lockdown path).
		fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA %q TO eurobase_readonly`, schemaName),
		fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %q TO eurobase_readonly`, schemaName),
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("lockdown readonly: %s: %w", s, err)
		}
	}
	logger.Info("readonly role locked down to SELECT-only on tenant schema",
		"schema", schemaName)
	return nil
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
