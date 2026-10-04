// Package tenantlogin gives each shared-cluster tenant's `<schema>_func`
// role its own login, so the edge-functions runner can connect *as the
// tenant* instead of switching roles on a shared connection (stage B,
// phase 1 — see AGENTS.md § Postgres roles).
//
// Passwords are derived, never stored: HMAC-SHA256(FUNC_PASSWORD_SECRET,
// "funcpw:"+schema), hex-encoded. The runner derives the same value to
// connect. The worker never sends the password itself to the server: it
// sets a pre-computed SCRAM-SHA-256 verifier with a salt that is also
// derived from the secret, so the statement text holds no plaintext and
// repeated runs write an identical value.
package tenantlogin

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MinSecretLen is the minimum FUNC_PASSWORD_SECRET length (bytes).
const MinSecretLen = 32

// FuncConnLimit is the per-tenant connection limit on `<schema>_func`.
// It must cover every pooled + direct consumer of a tenant's _func login:
//   - the runner pooler: replicas (2) × tenant pool (2) = 4;
//   - the gateway pooler (SDK customer SQL, from step 3): replicas × tenant
//     pool;
//   - DirectFuncConnections = 2 (one cron job on the worker, one console
//     dry run on the gateway), held outside the poolers;
//   - a spare for a pooler rollout surge.
//
// `pgbouncerconf.CheckTenantBudget` asserts this at startup and fails if
// the configured pools exceed it.
//
// Raised 8 → 16 (2026-10-04) to give the gateway pooler headroom for the
// SDK customer-SQL path (per-project _func logins, #702 / security
// remediation). Safe on the current fleet: this is a PER-TENANT cap and
// only *active* tenants hold connections; the hard limit is the shared
// cluster's max_connections = 100.
//
// FUTURE — if a larger user base makes the aggregate (concurrently active
// tenants × their held _func connections, across both poolers) approach
// max_connections = 100, the levers are, in order: (1) shrink the per-pool
// tenant pool sizes (PGB_TENANT_POOL_SIZE on each pooler) — the pooler
// already bounds aggregate server connections; (2) raise the shared
// cluster's max_connections (Scaleway RDB, needs a bigger instance);
// (3) move more projects to Team-tier dedicated instances (their own
// budget). Lowering FuncConnLimit again only works if the pools fit under
// it (CheckTenantBudget enforces that).
const FuncConnLimit = 16

// DedicatedFuncConnLimit is the limit on a Team project's dedicated
// instance (#677). There the runner connects directly — one connection
// per runner pod, not through PgBouncer's pool of 2 — so it must cover
// the functions HPA max (6) plus rollout surge (2), a cron job (worker,
// jobs run one at a time), a console dry run, and one spare. It fits the
// instance's platform budget (AGENTS.md, Team-tier routing).
const DedicatedFuncConnLimit = 12

// scramIterations matches PostgreSQL's default scram_iterations.
const scramIterations = 4096

var schemaRe = regexp.MustCompile(`^tenant_[0-9a-f_]+$`)

// FuncRole returns the per-tenant function role for a schema.
func FuncRole(schema string) string { return schema + "_func" }

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

// FuncPassword derives the login password for a tenant's function role.
// Must match functions-runner/tenant_db.ts funcPassword().
func FuncPassword(secret []byte, schema string) string {
	return hex.EncodeToString(hmacSHA256(secret, "funcpw:"+schema))
}

// DedicatedSubject is the derivation subject for a tenant's function role
// on a Team project's dedicated instance (#676): FuncPassword(secret,
// DedicatedSubject(id, schema)). Distinct per instance (project_databases.id),
// so a leaked Team password opens neither the shared cluster's role of the
// same schema (the stale copy after an upgrade) nor another instance of the
// project. Must match functions-runner/tenant_route.ts dedicatedSubject().
func DedicatedSubject(databaseID, schema string) string {
	return "ded:" + databaseID + ":" + schema
}

// ScramVerifier returns the SCRAM-SHA-256 verifier PostgreSQL stores for
// FuncPassword(secret, schema) (RFC 5802 / 7677). The salt is derived
// from the secret, so the result is deterministic.
func ScramVerifier(secret []byte, schema string) (string, error) {
	password := FuncPassword(secret, schema)
	salt := hmacSHA256(secret, "funcsalt:"+schema)[:16]
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, 32)
	if err != nil {
		return "", fmt.Errorf("pbkdf2: %w", err)
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s",
		scramIterations, b64(salt), b64(storedKey[:]), b64(serverKey)), nil
}

// Ensurer sets LOGIN + password + CONNECT on tenant function roles.
type Ensurer struct {
	adminPool *pgxpool.Pool // developer pool; statements run as eurobase_migrator
	database  string
	secret    []byte

	// PrepareDedicated, if set, runs on a Team project's owner pool before
	// its login is applied — the worker creates the function role there
	// when it's missing (instances bootstrapped before #676).
	PrepareDedicated func(ctx context.Context, ownerPool *pgxpool.Pool, schema string) error
	// GrantDedicatedConnect, if set, grants the function role CONNECT
	// through the provider (SetPrivilege) when EnsureDedicated reports
	// ErrNoConnect — and then re-applies the lockdown on ownerPool
	// (dbprovider.EnsureFuncRole); the login is re-verified after.
	GrantDedicatedConnect func(ctx context.Context, projectID, schema string, ownerPool *pgxpool.Pool) error

	mu       sync.Mutex
	ensured  map[string]bool // schemas done since the last full pass
	lastFull time.Time
}

// FullPassInterval is how often every tenant is re-applied even if it
// was ensured before. Heals out-of-band changes — including a tenant
// changing its own role's password from function SQL (Postgres lets a
// role do that), which only locks that tenant out of its own DB until
// the next full pass.
const FullPassInterval = 15 * time.Minute

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
	return &Ensurer{adminPool: adminPool, database: database, secret: secret, ensured: map[string]bool{}}, nil
}

// EnsureOne makes one tenant's function role loginable with its derived
// credentials and verifies CONNECT (a GRANT by a non-owner can silently
// no-op, so the check is authoritative).
func (e *Ensurer) EnsureOne(ctx context.Context, schema string) error {
	return ensureOn(ctx, e.adminPool, e.database, e.secret, schema, schema, FuncConnLimit, true)
}

// EnsureDedicated does the same on a Team project's dedicated instance
// (project_databases row databaseID)
// (#676), with the per-instance password (DedicatedSubject), through ownerPool (connected as the instance's owner, which
// created the role via ensure_tenant_func_role). CONNECT there comes from
// the provider's SetPrivilege (a GRANT by the owner is a no-op on
// Scaleway's rdb database); if it's missing the error says so.
func EnsureDedicated(ctx context.Context, ownerPool *pgxpool.Pool, database string, secret []byte, schema, databaseID string) error {
	if databaseID == "" {
		return errors.New("dedicated function login: no database id")
	}
	return ensureOn(ctx, ownerPool, database, secret, schema, DedicatedSubject(databaseID, schema), DedicatedFuncConnLimit, false)
}

func ensureOn(ctx context.Context, pool *pgxpool.Pool, database string, secret []byte, schema, subject string, connLimit int, asMigrator bool) error {
	if !schemaRe.MatchString(schema) {
		return fmt.Errorf("invalid tenant schema %q", schema)
	}
	role := FuncRole(schema)
	verifier, err := ScramVerifier(secret, subject)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Don't queue behind a concurrent role/DB change (provision_tenant,
	// tenant migrations); the next pass retries.
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '3s'"); err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}
	if asMigrator {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE eurobase_migrator"); err != nil {
			return fmt.Errorf("set migrator role: %w", err)
		}
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
		return fmt.Errorf("check role: %w", err)
	}
	if !exists {
		return nil // project without a function role (e.g. mid-provisioning)
	}
	// The verifier is [A-Za-z0-9+/=:$-] only — safe inside the literal.
	// CONNECTION LIMIT caps what a tenant can do with its own login if it
	// learns a password (a role may change its own password from function
	// SQL; it cannot change its connection limit). RESET ALL drops any
	// role-level defaults the tenant set on itself.
	if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s WITH LOGIN CONNECTION LIMIT %d PASSWORD '%s'",
		pgx.Identifier{role}.Sanitize(), connLimit, verifier)); err != nil {
		return fmt.Errorf("set login on %s: %w", role, err)
	}
	// Both scopes: role-wide and per-database defaults (a role may set
	// either on itself).
	if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s RESET ALL", pgx.Identifier{role}.Sanitize())); err != nil {
		return fmt.Errorf("reset role settings on %s: %w", role, err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s IN DATABASE %s RESET ALL",
		pgx.Identifier{role}.Sanitize(), pgx.Identifier{database}.Sanitize())); err != nil {
		return fmt.Errorf("reset per-database role settings on %s: %w", role, err)
	}
	var canConnect bool
	if err := tx.QueryRow(ctx, "SELECT has_database_privilege($1, $2, 'CONNECT')", role, database).Scan(&canConnect); err != nil {
		return fmt.Errorf("check connect: %w", err)
	}
	if !canConnect {
		if _, err := tx.Exec(ctx, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s",
			pgx.Identifier{database}.Sanitize(), pgx.Identifier{role}.Sanitize())); err != nil {
			return fmt.Errorf("grant connect to %s: %w", role, err)
		}
		if err := tx.QueryRow(ctx, "SELECT has_database_privilege($1, $2, 'CONNECT')", role, database).Scan(&canConnect); err != nil {
			return fmt.Errorf("verify connect: %w", err)
		}
		if !canConnect {
			if !asMigrator {
				// Commit the login anyway; CONNECT needs the provider API.
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				return fmt.Errorf("role %s on %s: %w", role, database, ErrNoConnect)
			}
			return fmt.Errorf("role %s has no CONNECT on %s and eurobase_migrator could not grant it", role, database)
		}
	}
	return tx.Commit(ctx)
}

// ErrNoConnect: the function role on a dedicated instance has its login
// but no CONNECT, which only the provider's SetPrivilege can grant there.
var ErrNoConnect = errors.New("no CONNECT on the database: grant it via the provider's SetPrivilege (readwrite)")

// DedicatedDB identifies the dedicated instance an opener connected to.
type DedicatedDB struct {
	ID       string // project_databases.id (the password subject)
	Database string
}

// DedicatedOpener opens a short-lived owner pool to a Team project's
// dedicated instance; (nil, _, nil) when the project has none active. The
// caller closes the pool.
type DedicatedOpener func(ctx context.Context, projectID string) (*pgxpool.Pool, DedicatedDB, error)

// dedicatedTimeout bounds one Team project in EnsureTeam, so an
// unreachable instance or a customer holding locks can't stall the pass
// (which also carries the shared-cluster logins).
const dedicatedTimeout = 30 * time.Second

// EnsureTeam applies EnsureDedicated to every Team project with an active
// dedicated instance (#676): the function role there gets LOGIN, the
// connection limit and its per-instance password. Same cadence as
// EnsureAll; each instance once per full pass.
func (e *Ensurer) EnsureTeam(ctx context.Context, open DedicatedOpener) (int, error) {
	if open == nil {
		return 0, nil
	}
	rows, err := e.adminPool.Query(ctx,
		`SELECT p.id::text, p.schema_name
		   FROM public.projects p
		   JOIN public.project_databases pd ON pd.project_id = p.id
		  WHERE pd.state = 'active' AND pd.deleted_at IS NULL
		    AND p.status = 'active' AND p.schema_name IS NOT NULL
		  ORDER BY 1`)
	if err != nil {
		return 0, fmt.Errorf("list Team projects: %w", err)
	}
	type target struct{ id, schema string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.schema); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var firstErr error
	done := 0
	for _, t := range targets {
		key, err := e.ensureTeamOne(ctx, open, t.id, t.schema)
		if err != nil {
			slog.Error("tenantlogin: ensure dedicated function role login failed", "project_id", t.id, "schema", t.schema, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if key != "" {
			done++
		}
	}
	return done, firstErr
}

// ensureTeamOne handles one project; it returns the cache key it ensured
// ("" when skipped).
func (e *Ensurer) ensureTeamOne(ctx context.Context, open DedicatedOpener, projectID, schema string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, dedicatedTimeout)
	defer cancel()
	pool, db, err := open(ctx, projectID)
	if err != nil || pool == nil {
		return "", err
	}
	defer pool.Close()
	// Per instance: a restore or move to a new instance is picked up at
	// once, not at the next full pass.
	key := "team:" + db.ID
	e.mu.Lock()
	skip := e.ensured[key]
	e.mu.Unlock()
	if skip {
		return "", nil
	}
	if e.PrepareDedicated != nil {
		if err := e.PrepareDedicated(ctx, pool, schema); err != nil {
			return "", err
		}
	}
	err = EnsureDedicated(ctx, pool, db.Database, e.secret, schema, db.ID)
	if errors.Is(err, ErrNoConnect) && e.GrantDedicatedConnect != nil {
		if err = e.GrantDedicatedConnect(ctx, projectID, schema, pool); err == nil {
			err = EnsureDedicated(ctx, pool, db.Database, e.secret, schema, db.ID)
		}
	}
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.ensured[key] = true
	e.mu.Unlock()
	return key, nil
}

// EnsureAll applies EnsureOne to shared-cluster tenant schemas: only new
// ones between full passes, every one at least once per FullPassInterval.
// Returns how many roles were ensured and the first error (it keeps going
// past per-tenant failures so one bad tenant can't block the rest).
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
			slog.Error("tenantlogin: ensure function role login failed", "schema", s, "error", err)
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
