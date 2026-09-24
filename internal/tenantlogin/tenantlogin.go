// Package tenantlogin gives each shared-cluster tenant's `<schema>_func`
// role its own login, so the edge-functions runner can connect *as the
// tenant* instead of switching roles on a shared connection (stage B,
// phase 1 — see AGENTS.md § Postgres roles).
//
// Passwords are derived, never stored: HMAC-SHA256(FUNC_PASSWORD_SECRET,
// "funcpw:"+schema), hex-encoded. The worker applies them; the runner
// derives the same value to connect. Same pattern as the per-tenant
// `_ddl` roles (DDL_PASSWORD_SECRET) and Team runtime logins.
package tenantlogin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MinSecretLen is the minimum FUNC_PASSWORD_SECRET length (bytes).
const MinSecretLen = 32

var schemaRe = regexp.MustCompile(`^tenant_[0-9a-f_]+$`)

// FuncRole returns the per-tenant function role for a schema.
func FuncRole(schema string) string { return schema + "_func" }

// FuncPassword derives the login password for a tenant's function role.
// Must match functions-runner/tenant_db.ts funcPassword().
func FuncPassword(secret []byte, schema string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("funcpw:" + schema))
	return hex.EncodeToString(mac.Sum(nil))
}

// Ensurer sets LOGIN + password + CONNECT on tenant function roles.
type Ensurer struct {
	adminPool *pgxpool.Pool // developer pool; statements run as eurobase_migrator
	database  string
	secret    []byte
}

// NewEnsurer returns nil when the secret is empty (feature off). A
// non-empty secret shorter than MinSecretLen is an error.
func NewEnsurer(adminPool *pgxpool.Pool, database string, secret []byte) (*Ensurer, error) {
	if len(secret) == 0 {
		return nil, nil
	}
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("FUNC_PASSWORD_SECRET too short: %d bytes, need at least %d", len(secret), MinSecretLen)
	}
	if adminPool == nil || database == "" {
		return nil, errors.New("tenantlogin: admin pool and database name are required")
	}
	return &Ensurer{adminPool: adminPool, database: database, secret: secret}, nil
}

// EnsureOne makes one tenant's function role loginable with its derived
// password and verifies CONNECT (a GRANT by a non-owner can silently
// no-op, so the check is authoritative).
func (e *Ensurer) EnsureOne(ctx context.Context, schema string) error {
	if !schemaRe.MatchString(schema) {
		return fmt.Errorf("invalid tenant schema %q", schema)
	}
	role := FuncRole(schema)
	tx, err := e.adminPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE eurobase_migrator"); err != nil {
		return fmt.Errorf("set migrator role: %w", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
		return fmt.Errorf("check role: %w", err)
	}
	if !exists {
		return nil // project without a function role (e.g. mid-provisioning)
	}
	// Password is hex (injection-safe); the role name is a quoted identifier.
	if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD '%s'",
		pgx.Identifier{role}.Sanitize(), FuncPassword(e.secret, schema))); err != nil {
		return fmt.Errorf("set login on %s: %w", role, err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s",
		pgx.Identifier{e.database}.Sanitize(), pgx.Identifier{role}.Sanitize())); err != nil {
		return fmt.Errorf("grant connect to %s: %w", role, err)
	}
	var canConnect bool
	if err := tx.QueryRow(ctx, "SELECT has_database_privilege($1, $2, 'CONNECT')", role, e.database).Scan(&canConnect); err != nil {
		return fmt.Errorf("verify connect: %w", err)
	}
	if !canConnect {
		return fmt.Errorf("role %s has no CONNECT on %s and eurobase_migrator could not grant it", role, e.database)
	}
	return tx.Commit(ctx)
}

// EnsureAll applies EnsureOne to every shared-cluster tenant schema.
// Returns how many roles were ensured and the first error (it keeps
// going past per-tenant failures so one bad tenant can't block the rest).
func (e *Ensurer) EnsureAll(ctx context.Context) (int, error) {
	rows, err := e.adminPool.Query(ctx,
		`SELECT n.nspname FROM pg_namespace n WHERE n.nspname ~ '^tenant_[0-9a-f_]+$' ORDER BY 1`)
	if err != nil {
		return 0, fmt.Errorf("list tenant schemas: %w", err)
	}
	schemas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("scan tenant schemas: %w", err)
	}
	var firstErr error
	done := 0
	for _, s := range schemas {
		if err := e.EnsureOne(ctx, s); err != nil {
			slog.Error("tenantlogin: ensure function role login failed", "schema", s, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		done++
	}
	return done, firstErr
}
