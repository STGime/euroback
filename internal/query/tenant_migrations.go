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

// tenantDDLRole is the per-tenant schema-scoped DDL role name (migration
// 000063): CREATE on its own schema only, no public.* table access, not a
// member of eurobase_migrator.
func tenantDDLRole(schemaName string) string { return schemaName + "_ddl" }

// ApplyTenantMigration applies one versioned migration against the
// project's tenant schema. Returns applied=false (and no error) when the
// identical version+checksum was already applied — re-runs are no-ops.
//
// runnerPool MUST connect as eurobase_ddl_runner — a low-privilege LOGIN
// role (NOINHERIT member of the per-tenant ddl roles, no public.* grants).
// Containment is by Postgres role privilege, NOT the validator:
//   - bookkeeping (lock, history check, insert) runs as ddl_runner, which
//     has SELECT/INSERT on public.tenant_migrations and nothing else;
//   - user SQL runs under SET LOCAL ROLE tenant_<id>_ddl, which owns only
//     its own schema — cross-schema and platform writes are denied even
//     from inside a DO/function body or a dynamic EXECUTE;
//   - a body that does RESET ROLE lands back on ddl_runner (the harmless
//     session role), NOT on a migrator-inheriting role — which is exactly
//     why this cannot run on the developer or gateway pool.
// The validator stays as defense-in-depth and for clearer error messages.
//
// One transaction: advisory-lock the project (serializes concurrent
// applies), run the user SQL via the simple protocol (multi-statement
// bodies are the point — the deliberate inverse of the cron-SQL
// single-statement hardening, GHSA-fjjq-cqq9-q793), record bookkeeping.
func ApplyTenantMigration(ctx context.Context, runnerPool *pgxpool.Pool, projectID, schemaName string, version int64, name, sqlText, appliedBy string) (applied bool, err error) {
	if version <= 0 {
		return false, errors.New("version must be a positive integer")
	}
	if !validIdentRe.MatchString(schemaName) {
		return false, fmt.Errorf("invalid schema name")
	}
	if err := ValidateTenantMigrationSQL(sqlText); err != nil {
		return false, err
	}
	checksum := MigrationChecksum(sqlText)
	ddlRole := tenantDDLRole(schemaName)

	tx, err := runnerPool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Serialize concurrent applies per project for the duration of the
	// transaction. Without this, two `migrations up` runs could each see
	// version N as unapplied and race the user SQL.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 190))", projectID); err != nil {
		return false, fmt.Errorf("acquire migration lock: %w", err)
	}

	// Idempotency / tamper check against the recorded history (as ddl_runner).
	var existing string
	err = tx.QueryRow(ctx,
		`SELECT checksum FROM public.tenant_migrations WHERE project_id = $1 AND version = $2`,
		projectID, version,
	).Scan(&existing)
	switch {
	case err == nil:
		if existing == checksum {
			return false, nil // already applied, identical — no-op
		}
		return false, ErrMigrationChecksumMismatch
	case errors.Is(err, pgx.ErrNoRows):
		// not applied yet — proceed
	default:
		return false, fmt.Errorf("check migration history: %w", err)
	}

	// Drop into the per-tenant DDL role and pin the execution context.
	// search_path resolves unqualified names to the tenant schema;
	// statement_timeout bounds runaway DDL.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL ROLE %s", pgx.Identifier{ddlRole}.Sanitize())); err != nil {
		return false, fmt.Errorf("set role %s: %w", ddlRole, err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", pgx.Identifier{schemaName}.Sanitize())); err != nil {
		return false, fmt.Errorf("set search_path: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '60s'"); err != nil {
		return false, fmt.Errorf("set statement_timeout: %w", err)
	}

	// User SQL runs as tenant_<id>_ddl. Simple query protocol so the whole
	// multi-statement body executes in this transaction.
	if _, err := tx.Conn().PgConn().Exec(ctx, sqlText).ReadAll(); err != nil {
		return false, fmt.Errorf("migration failed: %w", err)
	}

	// Return to ddl_runner for bookkeeping, regardless of any role the body
	// left set (it can only SET ROLE within tenant_<id>_ddl's memberships,
	// none privileged; RESET lands on ddl_runner either way).
	if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
		return false, fmt.Errorf("reset role: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO public.tenant_migrations (project_id, version, name, sql, checksum, applied_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		projectID, version, name, sqlText, checksum, appliedBy,
	); err != nil {
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
