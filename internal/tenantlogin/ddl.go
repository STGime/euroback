package tenantlogin

// Per-tenant `<schema>_ddl` logins (step 6 of per-project SQL logins).
//
// `<schema>_ddl` (migration 000063) owns the tenant's application tables
// and is a member of nothing. Tenant migrations already connect as it,
// switching its login on and off around each run. With a persistent login
// (DDL_PERSISTENT_LOGIN=1) the worker keeps it loginable like `_func`, so
// platform SQL can run on short-lived direct connections as the project's
// own role, with no on/off race between concurrent requests.
//
// Password: hex(HMAC-SHA256(DDL_PASSWORD_SECRET, "ddlpw:"+schema)) — the
// same derivation tenant migrations use, so both paths share one login.
// The worker sets a SCRAM verifier (salt derived from the secret), never
// the plaintext.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DDLConnLimit is the per-tenant connection limit on `<schema>_ddl`. Its
// consumers are short-lived direct connections from the gateway (console
// and MCP SQL, schema changes, migrations), capped per gateway pod as
// well; this bounds what one tenant can hold if it learns its password.
const DDLConnLimit = 4

// PersistentDDLLogin reports whether DDL_PERSISTENT_LOGIN is on. Off: the
// worker leaves `_ddl` alone and tenant migrations switch its login on and
// off per run (the original behaviour). Read by the worker and the gateway
// — set it on both.
func PersistentDDLLogin() bool { return os.Getenv("DDL_PERSISTENT_LOGIN") == "1" }

// DDLRole returns the per-tenant DDL role for a schema.
func DDLRole(schema string) string { return schema + "_ddl" }

// DDLPassword derives the login password for a tenant's DDL role. Must
// match query.MigrationExecutor's derivation.
func DDLPassword(secret []byte, schema string) string {
	return hex.EncodeToString(hmacSHA256(secret, "ddlpw:"+schema))
}

// DDLScramVerifier returns the SCRAM-SHA-256 verifier for DDLPassword.
func DDLScramVerifier(secret []byte, schema string) (string, error) {
	return scramVerifier(DDLPassword(secret, schema), hmacSHA256(secret, "ddlsalt:"+schema)[:16])
}

// EnsureDDLLogin makes a tenant's `_ddl` role loginable with its derived
// credentials (LOGIN, DDLConnLimit, verifier, settings reset, CONNECT) and
// keeps its access in its own schema: USAGE on public (name resolution of
// the RLS helpers), and the same table / sequence / function privileges
// the tenant's `_func` role has — including the platform-managed system
// tables, which `_ddl` can read and write (subject to their RLS) but not
// alter. Runs as eurobase_migrator through adminPool (the developer pool).
func EnsureDDLLogin(ctx context.Context, adminPool *pgxpool.Pool, database string, secret []byte, schema string) error {
	if !schemaRe.MatchString(schema) {
		return fmt.Errorf("invalid tenant schema %q", schema)
	}
	verifier, err := DDLScramVerifier(secret, schema)
	if err != nil {
		return err
	}
	role := DDLRole(schema)
	return ensureLogin(ctx, adminPool, database, role, verifier, DDLConnLimit, true, func(ctx context.Context, tx pgx.Tx) error {
		return grantDDLAccess(ctx, tx, schema, role)
	})
}

func grantDDLAccess(ctx context.Context, tx pgx.Tx, schema, role string) error {
	r := pgx.Identifier{role}.Sanitize()
	sc := pgx.Identifier{schema}.Sanitize()
	stmts := []string{
		"GRANT USAGE ON SCHEMA public TO " + r,
		// Objects in the tenant schema owned by eurobase_migrator (system
		// tables, objects created before the role owned new ones). Grants on
		// objects the role owns are no-ops.
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s", sc, r),
		fmt.Sprintf("GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %s TO %s", sc, r),
		fmt.Sprintf("GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %s TO %s", sc, r),
		// And future ones the migrator creates there.
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", sc, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %s GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %s", sc, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %s GRANT EXECUTE ON FUNCTIONS TO %s", sc, r),
	}
	for _, q := range stmts {
		if _, err := tx.Exec(ctx, q); err != nil {
			return fmt.Errorf("grant %s access: %w", role, err)
		}
	}
	// A GRANT on a Scaleway-owned object by a non-owner is a WARNING, not an
	// error: verify the one that matters for name resolution.
	var publicUsage bool
	if err := tx.QueryRow(ctx, "SELECT has_schema_privilege($1, 'public', 'USAGE')", role).Scan(&publicUsage); err != nil {
		return fmt.Errorf("verify public usage: %w", err)
	}
	if !publicUsage {
		return fmt.Errorf("role %s has no USAGE on schema public and eurobase_migrator could not grant it (needs USAGE … WITH GRANT OPTION from the bootstrap owner)", role)
	}
	return nil
}

// DDLEnsurer keeps every shared-cluster tenant's `_ddl` role loginable.
// Team projects keep running platform SQL as their dedicated instance's
// owner, so there is nothing to do there.
type DDLEnsurer struct {
	adminPool *pgxpool.Pool
	database  string
	secret    []byte

	mu       sync.Mutex
	ensured  map[string]bool
	lastFull time.Time
}

// NewDDLEnsurer returns nil when the secret is empty (tenant migrations
// are off too). A non-empty secret shorter than MinSecretLen is an error.
func NewDDLEnsurer(adminPool *pgxpool.Pool, database string, secret []byte) (*DDLEnsurer, error) {
	if len(secret) == 0 {
		return nil, nil
	}
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("DDL_PASSWORD_SECRET too short: %d bytes, need at least %d", len(secret), MinSecretLen)
	}
	if adminPool == nil || database == "" {
		return nil, errors.New("tenantlogin: admin pool and database name are required")
	}
	return &DDLEnsurer{adminPool: adminPool, database: database, secret: secret, ensured: map[string]bool{}}, nil
}

// EnsureOne applies EnsureDDLLogin to one tenant.
func (e *DDLEnsurer) EnsureOne(ctx context.Context, schema string) error {
	return EnsureDDLLogin(ctx, e.adminPool, e.database, e.secret, schema)
}

// EnsureAll applies EnsureOne to shared-cluster tenant schemas: new ones
// between full passes, every one at least once per FullPassInterval (which
// also undoes a password a tenant set on its own role).
func (e *DDLEnsurer) EnsureAll(ctx context.Context) (int, error) {
	rows, err := e.adminPool.Query(ctx,
		`SELECT n.nspname FROM pg_namespace n WHERE n.nspname ~ '^tenant_[0-9a-f_]+$' ORDER BY 1`)
	if err != nil {
		return 0, fmt.Errorf("list tenant schemas: %w", err)
	}
	schemas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("scan tenant schemas: %w", err)
	}

	e.mu.Lock()
	if time.Since(e.lastFull) >= FullPassInterval {
		e.ensured = map[string]bool{}
		e.lastFull = time.Now()
	}
	e.mu.Unlock()

	var firstErr error
	done := 0
	for _, s := range schemas {
		e.mu.Lock()
		skip := e.ensured[s]
		e.mu.Unlock()
		if skip {
			continue
		}
		if err := e.EnsureOne(ctx, s); err != nil {
			slog.Error("tenantlogin: ensure DDL role login failed", "schema", s, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		e.mu.Lock()
		e.ensured[s] = true
		e.mu.Unlock()
		done++
	}
	return done, firstErr
}
