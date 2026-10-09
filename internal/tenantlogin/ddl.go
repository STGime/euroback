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
	if _, err := tx.Exec(ctx, "GRANT USAGE ON SCHEMA public TO "+r); err != nil {
		return fmt.Errorf("grant %s public usage: %w", role, err)
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

	// Per object, the same privileges provision_tenant gives `_func`:
	// tables DML, sequences USAGE + SELECT, functions EXECUTE. Only objects
	// whose owner the migrator can act for (a GRANT on anything else is an
	// error, e.g. legacy developer-owned tables), and only where the role
	// lacks the privilege, so passes don't rewrite the catalog.
	rows, err := tx.Query(ctx, `
		SELECT 'TABLE', c.oid::regclass::text, pg_has_role(current_user, c.relowner, 'USAGE')
		  FROM pg_class c
		 WHERE c.relnamespace = $1::regnamespace
		   AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		   AND NOT (has_table_privilege($2, c.oid, 'SELECT') AND has_table_privilege($2, c.oid, 'INSERT')
		        AND has_table_privilege($2, c.oid, 'UPDATE') AND has_table_privilege($2, c.oid, 'DELETE'))
		UNION ALL
		SELECT 'SEQUENCE', c.oid::regclass::text, pg_has_role(current_user, c.relowner, 'USAGE')
		  FROM pg_class c
		 WHERE c.relnamespace = $1::regnamespace AND c.relkind = 'S'
		   AND NOT (has_sequence_privilege($2, c.oid, 'USAGE') AND has_sequence_privilege($2, c.oid, 'SELECT'))
		UNION ALL
		SELECT 'ROUTINE', p.oid::regprocedure::text, pg_has_role(current_user, p.proowner, 'USAGE')
		  FROM pg_proc p
		 WHERE p.pronamespace = $1::regnamespace AND p.prokind IN ('f', 'p')
		   AND NOT has_function_privilege($2, p.oid, 'EXECUTE')`, schema, role)
	if err != nil {
		return fmt.Errorf("list %s objects: %w", schema, err)
	}
	type object struct{ kind, name string }
	var grantable []object
	skipped := 0
	for rows.Next() {
		var o object
		var canGrant bool
		if err := rows.Scan(&o.kind, &o.name, &canGrant); err != nil {
			rows.Close()
			return err
		}
		if canGrant {
			grantable = append(grantable, o)
		} else {
			skipped++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	privs := map[string]string{"TABLE": "SELECT, INSERT, UPDATE, DELETE", "SEQUENCE": "USAGE, SELECT", "ROUTINE": "EXECUTE"}
	for _, o := range grantable {
		// o.name is a regclass / regprocedure rendering: already quoted.
		if _, err := tx.Exec(ctx, fmt.Sprintf("GRANT %s ON %s %s TO %s", privs[o.kind], o.kind, o.name, r)); err != nil {
			return fmt.Errorf("grant %s on %s: %w", role, o.name, err)
		}
	}
	if skipped > 0 {
		slog.Warn("tenantlogin: objects the DDL role can't be granted access to (owner outside eurobase_migrator's reach)",
			"schema", schema, "count", skipped)
	}

	// Future objects the migrator creates there (as provision_tenant does
	// for `_func`).
	sc := pgx.Identifier{schema}.Sanitize()
	for _, q := range []string{
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", sc, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %s GRANT USAGE, SELECT ON SEQUENCES TO %s", sc, r),
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			return fmt.Errorf("default privileges for %s: %w", role, err)
		}
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
	// Each role's live state too: a login switched off (e.g. by a gateway
	// running without the flag) or a changed limit is repaired on this
	// pass, not only at the next full one.
	rows, err := e.adminPool.Query(ctx,
		`SELECT n.nspname, COALESCE(r.rolcanlogin AND r.rolconnlimit = $1, false)
		   FROM pg_namespace n
		   LEFT JOIN pg_roles r ON r.rolname = n.nspname || '_ddl'
		  WHERE n.nspname ~ '^tenant_[0-9a-f_]+$'
		  ORDER BY 1`, DDLConnLimit)
	if err != nil {
		return 0, fmt.Errorf("list tenant schemas: %w", err)
	}
	type state struct {
		schema string
		ok     bool
	}
	var schemas []state
	for rows.Next() {
		var st state
		if err := rows.Scan(&st.schema, &st.ok); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan tenant schemas: %w", err)
		}
		schemas = append(schemas, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	e.mu.Lock()
	if time.Since(e.lastFull) >= FullPassInterval {
		e.ensured = map[string]bool{}
		e.lastFull = time.Now()
	}
	e.mu.Unlock()

	var firstErr error
	done := 0
	for _, st := range schemas {
		e.mu.Lock()
		skip := e.ensured[st.schema] && st.ok
		e.mu.Unlock()
		if skip {
			continue
		}
		if err := e.EnsureOne(ctx, st.schema); err != nil {
			slog.Error("tenantlogin: ensure DDL role login failed", "schema", st.schema, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		e.mu.Lock()
		e.ensured[st.schema] = true
		e.mu.Unlock()
		done++
	}
	return done, firstErr
}
