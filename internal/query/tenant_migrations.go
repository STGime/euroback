package query

// Tenant-level schema migrations — closes #190.
//
// Tenants previously had no sanctioned way to run versioned schema
// migrations: /v1/db/sql is SELECT-only, the DDL REST endpoints are
// imperative and unversioned, and `eurobase migrations` was a platform
// tool. This adds a single chokepoint through which every channel (CLI,
// console, MCP) applies versioned SQL against the tenant schema.
//
// Trust model (see the design comment on #190): platform plane only.
// The endpoint requires platform auth (PAT / console session, developer
// role minimum) — the same plane that already gets migrator-powered DDL
// via /platform/.../schema/*. Data-plane keys (anon/secret) deliberately
// cannot run migrations: a leaked secret key must never escalate from
// data exposure to schema destruction.
//
// Containment: the migration runs as eurobase_migrator with search_path
// pinned to the tenant schema, in one transaction, guarded by a
// per-project advisory lock. The validator below is defense-in-depth on
// top of that — it rejects the known escape hatches rather than trying
// to parse SQL.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantMigration is one applied migration row.
type TenantMigration struct {
	Version   int64     `json:"version"`
	Name      string    `json:"name"`
	Checksum  string    `json:"checksum"`
	AppliedBy string    `json:"applied_by,omitempty"`
	AppliedAt time.Time `json:"applied_at"`
}

// maxMigrationSQLBytes bounds a single migration body. Large data loads
// belong in the data plane, not the migration channel.
const maxMigrationSQLBytes = 512 * 1024

// publicHelperAllowRe matches the public.* helper functions that tenant
// SQL legitimately references — RLS policy presets are built from
// public.is_service_role() / public.current_end_user_id(), table
// defaults use public.uuid_generate_v4(), and the sensitive-table
// policies use public.is_internal_auth_path(). These are blanked before
// the forbidden-schema scan so the general `public.` ban doesn't reject
// every realistic RLS policy.
var publicHelperAllowRe = regexp.MustCompile(
	`(?i)\bpublic\s*\.\s*(is_service_role|current_end_user_id|is_internal_auth_path|uuid_generate_v4)\b`,
)

// forbiddenMigSchemaRe rejects qualified references to schemas a tenant
// migration must not reach: the platform's public schema, catalogs, and
// any tenant schema (including the caller's own — search_path is pinned,
// so qualification is never needed; forbidding it keeps the regex free
// of per-request state).
var forbiddenMigSchemaRe = regexp.MustCompile(
	`(?i)\b(public|pg_catalog|information_schema|tenant_[a-z0-9_]+)\s*\.`,
)

// forbiddenMigStatementRes are escape hatches rejected outright. The
// scan runs on text with string literals, dollar-quoted bodies, and
// comments blanked, so function bodies don't false-positive — and,
// deliberately, are not scanned either: developer-authored function
// bodies are an accepted surface (the console's RPC-function endpoint
// already creates them under migrator). SECURITY DEFINER is the
// exception that must stay visible: it appears outside the body and
// would permanently capture migrator privileges in a tenant-callable
// function.
var forbiddenMigStatementRes = []struct {
	re  *regexp.Regexp
	msg string
}{
	{regexp.MustCompile(`(?i)\bsecurity\s+definer\b`), "SECURITY DEFINER functions are not allowed in migrations"},
	{regexp.MustCompile(`(?i)\b(commit|rollback|savepoint|prepare\s+transaction)\b`), "transaction control statements are not allowed — the migration already runs in a transaction"},
	{regexp.MustCompile(`(?i)\bbegin\s*;`), "transaction control statements are not allowed — the migration already runs in a transaction"},
	{regexp.MustCompile(`(?i)\b(set|reset)\s+(local\s+|session\s+)?(role|session_authorization|search_path)\b`), "changing role or search_path is not allowed in migrations"},
	{regexp.MustCompile(`(?i)\b(create|alter|drop)\s+(role|user|group|database|extension|event\s+trigger|publication|subscription|tablespace|server|foreign\s+data\s+wrapper)\b`), "managing roles, databases, extensions, or server objects is not allowed in migrations"},
	{regexp.MustCompile(`(?i)\b(create|drop|alter)\s+schema\b`), "schema management is not allowed — migrations run inside your project schema"},
	{regexp.MustCompile(`(?i)\b(grant|revoke)\b`), "GRANT/REVOKE are not allowed in migrations — access control is managed by the platform"},
	{regexp.MustCompile(`(?i)\balter\s+system\b`), "ALTER SYSTEM is not allowed"},
	{regexp.MustCompile(`(?i)\bcopy\b`), "COPY is not allowed in migrations — load data via the SDK or REST API"},
}

// ValidateTenantMigrationSQL rejects migration SQL that reaches outside
// the tenant schema or uses statements the channel forbids. Exported for
// the handler and tests.
func ValidateTenantMigrationSQL(sql string) error {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return errors.New("migration sql is empty")
	}
	if len(trimmed) > maxMigrationSQLBytes {
		return fmt.Errorf("migration sql exceeds %d bytes", maxMigrationSQLBytes)
	}

	stripped := stripSQLLiterals(trimmed)
	// Blank the allowlisted public helpers, then scan for any remaining
	// qualified reference to a forbidden schema.
	scanned := publicHelperAllowRe.ReplaceAllString(stripped, "ALLOWED_HELPER")
	if loc := forbiddenMigSchemaRe.FindString(scanned); loc != "" {
		return fmt.Errorf("migration references a forbidden schema (%s...): only unqualified names in your project schema are allowed (RLS helpers like public.is_service_role() are exempt)", strings.TrimSpace(loc))
	}
	for _, f := range forbiddenMigStatementRes {
		if f.re.MatchString(scanned) {
			return errors.New(f.msg)
		}
	}
	return nil
}

// MigrationChecksum is the canonical checksum of a migration body —
// sha256 over the trimmed SQL, hex-encoded. Used to make re-applies
// idempotent and to detect edited history.
func MigrationChecksum(sql string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sql)))
	return hex.EncodeToString(sum[:])
}

// ErrMigrationChecksumMismatch is returned when a version is re-applied
// with different SQL than the recorded application.
var ErrMigrationChecksumMismatch = errors.New("this version was already applied with different sql — bump the version instead of editing an applied migration")

// tenantDDLRole is the per-tenant schema-scoped DDL LOGIN role name
// (migration 000063): owns its own schema's application tables, member of
// nothing, no public.* table access. The gateway connects AS this role to
// run a migration, so RESET ROLE inside a body lands on the same role and
// cannot pivot into another tenant.
func tenantDDLRole(schemaName string) string { return schemaName + "_ddl" }

// MigrationExecutor runs tenant migrations under per-tenant LOGIN roles.
//
// adminPool connects as eurobase_developer (member of migrator) and is
// used only to set the per-tenant role's login password (SET LOCAL ROLE
// eurobase_migrator; ALTER ROLE …). baseConnConfig is a template parsed
// from DATABASE_URL — each apply clones it, overrides User/Password to the
// per-tenant ddl role, and opens a short-lived connection so the migration
// runs as a role that can reach exactly one tenant. passwordSecret derives
// each role's password (HMAC-SHA256, hex — injection-safe in the ALTER
// ROLE literal). Nil/empty secret ⇒ Enabled()==false ⇒ the endpoint fails
// closed; migrations never run on a privileged pool.
type MigrationExecutor struct {
	adminPool      *pgxpool.Pool
	baseConnConfig *pgx.ConnConfig
	passwordSecret []byte
}

// NewMigrationExecutor builds an executor. Returns a disabled executor
// (Enabled()==false) when secret is empty or baseDSN can't be parsed.
func NewMigrationExecutor(adminPool *pgxpool.Pool, baseDSN string, secret []byte) *MigrationExecutor {
	e := &MigrationExecutor{adminPool: adminPool, passwordSecret: secret}
	if len(secret) == 0 || baseDSN == "" {
		return e
	}
	cfg, err := pgx.ParseConfig(baseDSN)
	if err != nil {
		return &MigrationExecutor{adminPool: adminPool} // disabled
	}
	e.baseConnConfig = cfg
	return e
}

// Enabled reports whether tenant migrations can run (secret + base DSN +
// admin pool all present).
func (e *MigrationExecutor) Enabled() bool {
	return e != nil && len(e.passwordSecret) > 0 && e.baseConnConfig != nil && e.adminPool != nil
}

// ddlRolePassword derives the per-tenant login password — HMAC-SHA256 of
// the schema under the shared secret, hex-encoded (safe to interpolate
// into an ALTER ROLE … PASSWORD literal).
func (e *MigrationExecutor) ddlRolePassword(schemaName string) string {
	mac := hmac.New(sha256.New, e.passwordSecret)
	mac.Write([]byte("ddlpw:" + schemaName))
	return hex.EncodeToString(mac.Sum(nil))
}

// setRoleLogin promotes (login=true, sets the password) or demotes
// (login=false, NOLOGIN) the per-tenant ddl role, run as migrator via the
// admin pool. ddlRole is a validated identifier; password is hex.
//
// On promote it also (re)grants CONNECT and verifies it actually took:
// GRANT CONNECT ON DATABASE as a non-owner migrator is a silent no-op
// (WARNING, not error), so if PUBLIC's CONNECT is revoked and migrator
// doesn't own the DB the role can't connect. has_database_privilege is
// true iff the role can connect (via the grant OR PUBLIC), so it's the
// authoritative check — fail loud here rather than with an opaque
// "permission denied for database" at connect time.
func (e *MigrationExecutor) setRoleLogin(ctx context.Context, ddlRole, password string, login bool) error {
	tx, err := e.adminPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin admin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE eurobase_migrator"); err != nil {
		return fmt.Errorf("set migrator role: %w", err)
	}
	if !login {
		if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s WITH NOLOGIN", pgx.Identifier{ddlRole}.Sanitize())); err != nil {
			return fmt.Errorf("demote ddl role: %w", err)
		}
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD '%s'", pgx.Identifier{ddlRole}.Sanitize(), password)); err != nil {
		return fmt.Errorf("promote ddl role: %w", err)
	}
	db := e.baseConnConfig.Database
	if _, err := tx.Exec(ctx, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s",
		pgx.Identifier{db}.Sanitize(), pgx.Identifier{ddlRole}.Sanitize())); err != nil {
		return fmt.Errorf("grant connect: %w", err)
	}
	var canConnect bool
	if err := tx.QueryRow(ctx, "SELECT has_database_privilege($1, $2, 'CONNECT')", ddlRole, db).Scan(&canConnect); err != nil {
		return fmt.Errorf("verify connect privilege: %w", err)
	}
	if !canConnect {
		return fmt.Errorf("tenant migrations misconfigured: role %q has no CONNECT on database %q and eurobase_migrator could not grant it — make eurobase_migrator the owner of the %q database (or grant it CONNECT … WITH GRANT OPTION) via the bootstrap owner", ddlRole, db, db)
	}
	return tx.Commit(ctx)
}

// Apply applies one versioned migration against the project's tenant
// schema. Returns applied=false (no error) when the identical
// version+checksum was already applied — re-runs are no-ops.
//
// Containment is by Postgres role privilege, not the validator. The
// gateway promotes tenant_<id>_ddl to LOGIN with a derived password (via
// the admin pool, as migrator), then opens a short-lived connection AS
// that role. The role owns only its own schema and is a member of nothing,
// so a body's UPDATE public.projects / tenant_other.* / RESET-ROLE pivot
// all fail at the permission layer. Bookkeeping uses the session_user-
// bound SECURITY DEFINER helpers (000063) — the role has no direct grant
// on public.tenant_migrations, so a body can neither forge nor read other
// projects' history. The validator stays as defense-in-depth.
//
// One transaction on the per-tenant connection: advisory-lock the project,
// idempotency check, run the body via the simple protocol (multi-statement
// is the point — the deliberate inverse of the cron-SQL single-statement
// hardening, GHSA-fjjq-cqq9-q793), record via the helper.
func (e *MigrationExecutor) Apply(ctx context.Context, projectID, schemaName string, version int64, name, sqlText string) (applied bool, err error) {
	if !e.Enabled() {
		return false, errors.New("tenant migrations are not enabled on this deployment")
	}
	if version <= 0 {
		return false, errors.New("version must be a positive integer")
	}
	if !validIdentRe.MatchString(schemaName) {
		return false, errors.New("invalid schema name")
	}
	if err := ValidateTenantMigrationSQL(sqlText); err != nil {
		return false, err
	}
	checksum := MigrationChecksum(sqlText)
	ddlRole := tenantDDLRole(schemaName)
	password := e.ddlRolePassword(schemaName)

	// Promote the per-tenant role to LOGIN with the derived password, run
	// the migration, then demote it back to NOLOGIN. Keeping the role
	// NOLOGIN except during an active apply means a leaked DDL_PASSWORD_SECRET
	// (or a derived password that leaked into the DB log via the ALTER ROLE
	// statement) can't be used to log in outside the brief apply window.
	cfg := e.baseConnConfig.Copy()
	cfg.User = ddlRole
	cfg.Password = password

	var conn *pgx.Conn
	// One retry covers the race where a concurrent apply for the same tenant
	// demoted the role to NOLOGIN between our promote and connect.
	for attempt := 0; attempt < 2; attempt++ {
		if err = e.setRoleLogin(ctx, ddlRole, password, true); err != nil {
			return false, err
		}
		conn, err = pgx.ConnectConfig(ctx, cfg)
		if err == nil {
			break
		}
		if attempt == 1 {
			return false, fmt.Errorf("connect as %s: %w", ddlRole, err)
		}
	}
	// Demote back to NOLOGIN on the way out (best-effort; uses the parent
	// ctx so a cancelled request still attempts the lockdown).
	defer func() {
		_ = e.setRoleLogin(context.WithoutCancel(ctx), ddlRole, "", false)
	}()
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Serialize concurrent applies for this project.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 190))", projectID); err != nil {
		return false, fmt.Errorf("acquire migration lock: %w", err)
	}

	// Idempotency / tamper check via the session_user-bound helper (the
	// role has no direct SELECT on public.tenant_migrations).
	var existing *string
	if err := tx.QueryRow(ctx, "SELECT public.tenant_migration_checksum($1)", version).Scan(&existing); err != nil {
		return false, fmt.Errorf("check migration history: %w", err)
	}
	if existing != nil {
		if *existing == checksum {
			return false, nil // already applied, identical — no-op
		}
		return false, ErrMigrationChecksumMismatch
	}

	// Pin the execution context and run the body. current_user is already
	// tenant_<id>_ddl (we connected as it), so no SET ROLE is needed —
	// and a body's RESET ROLE lands right back here.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", pgx.Identifier{schemaName}.Sanitize())); err != nil {
		return false, fmt.Errorf("set search_path: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '60s'"); err != nil {
		return false, fmt.Errorf("set statement_timeout: %w", err)
	}
	if _, err := conn.PgConn().Exec(ctx, sqlText).ReadAll(); err != nil {
		return false, fmt.Errorf("migration failed: %w", err)
	}

	// Record via the session_user-bound SECURITY DEFINER helper.
	if _, err := tx.Exec(ctx, "SELECT public.record_tenant_migration($1, $2, $3, $4)",
		version, name, sqlText, checksum); err != nil {
		return false, fmt.Errorf("record migration: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit migration: %w", err)
	}
	return true, nil
}

// ListTenantMigrations returns the applied-migration history for a project.
func ListTenantMigrations(ctx context.Context, pool *pgxpool.Pool, projectID string) ([]TenantMigration, error) {
	rows, err := pool.Query(ctx,
		`SELECT version, name, checksum, COALESCE(applied_by, ''), applied_at
		 FROM public.tenant_migrations WHERE project_id = $1 ORDER BY version`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	defer rows.Close()

	out := []TenantMigration{}
	for rows.Next() {
		var m TenantMigration
		if err := rows.Scan(&m.Version, &m.Name, &m.Checksum, &m.AppliedBy, &m.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan migration: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// stripSQLLiterals blanks single-quoted strings, double-quoted
// identifiers, dollar-quoted bodies, and comments so token scans don't
// false-positive on quoted text. Mirrors internal/cron's scanner (which
// in turn mirrors multistmt.go); kept local to avoid an import cycle.
func stripSQLLiterals(sql string) string {
	out := make([]byte, 0, len(sql))
	blank := func(n int) {
		for i := 0; i < n; i++ {
			out = append(out, ' ')
		}
	}
	s := sql
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '\'':
			j := i + 1
			for j < n {
				if s[j] == '\'' {
					if j+1 < n && s[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			blank(j - i)
			i = j
		case c == '"':
			j := i + 1
			for j < n && s[j] != '"' {
				j++
			}
			if j < n {
				j++
			}
			blank(j - i)
			i = j
		case c == '$':
			// dollar-quote: $tag$ ... $tag$
			j := i + 1
			for j < n && (s[j] == '_' || isAlnumByte(s[j])) {
				j++
			}
			if j < n && s[j] == '$' {
				tag := s[i : j+1]
				end := strings.Index(s[j+1:], tag)
				if end >= 0 {
					total := (j + 1 + end + len(tag)) - i
					blank(total)
					i += total
					continue
				}
				// unterminated dollar quote: blank the rest
				blank(n - i)
				i = n
				continue
			}
			out = append(out, c)
			i++
		case c == '-' && i+1 < n && s[i+1] == '-':
			j := i
			for j < n && s[j] != '\n' {
				j++
			}
			blank(j - i)
			i = j
		case c == '/' && i+1 < n && s[i+1] == '*':
			depth := 1
			j := i + 2
			for j < n && depth > 0 {
				if j+1 < n && s[j] == '/' && s[j+1] == '*' {
					depth++
					j += 2
				} else if j+1 < n && s[j] == '*' && s[j+1] == '/' {
					depth--
					j += 2
				} else {
					j++
				}
			}
			blank(j - i)
			i = j
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

func isAlnumByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
