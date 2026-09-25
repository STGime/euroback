// Package pgbouncerconf renders PgBouncer's config and auth file for the
// shared cluster (#641).
//
// PgBouncer runs in transaction mode and authenticates every client
// itself (auth_type = scram-sha-256) against a userlist we generate:
//   - platform roles (gateway, developer, function runner) with the
//     plaintext passwords from their DATABASE_URL* — PgBouncer needs
//     them to log in to Postgres on the client's behalf;
//   - one line per tenant `<schema>_func` role holding the SCRAM verifier
//     internal/tenantlogin derives (identical to the one the worker sets
//     on the role), so PgBouncer can pass SCRAM through to Postgres
//     without ever holding a tenant's plaintext password.
//
// auth_query is not an option: it needs read access to pg_authid, which
// Scaleway does not grant.
package pgbouncerconf

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/jackc/pgx/v5"
)

// Settings tunes the rendered pgbouncer.ini.
type Settings struct {
	// Upstream (Postgres) — from the gateway's DATABASE_URL.
	Host     string
	Port     int
	Database string
	// ServerTLS is PgBouncer's server_tls_sslmode ("require" in prod).
	ServerTLS string
	// AuthFile is the path PgBouncer reads the userlist from.
	AuthFile string
	// MaxDBConnections caps server connections per PgBouncer instance for
	// the platform alias (Database: gateway / runner / developer roles).
	MaxDBConnections int
	// TenantMaxDBConnections caps server connections per instance for the
	// tenant alias (TenantDatabase), separately, so busy tenants can never
	// take the platform roles' connections (#641 PR 3).
	TenantMaxDBConnections int
	// QueryWaitTimeout is how long a client queues for a server connection
	// before PgBouncer fails its query (seconds).
	QueryWaitTimeout int
	// TenantPoolSize is the per-tenant pool (default_pool_size) per
	// replica. Replicas × TenantPoolSize plus the role's direct users must
	// fit in tenantlogin.FuncConnLimit (see CheckTenantBudget).
	TenantPoolSize int
	// Replicas is the number of PgBouncer instances.
	Replicas int
	// PlatformPoolSizes sets a per-user pool for the platform roles.
	PlatformPoolSizes map[string]int
}

// UpstreamFromURL fills Host/Port/Database from a postgres:// URL.
func (s *Settings) UpstreamFromURL(databaseURL string) error {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	s.Host = u.Hostname()
	s.Port = 5432
	if p := u.Port(); p != "" {
		if s.Port, err = strconv.Atoi(p); err != nil {
			return fmt.Errorf("parse port: %w", err)
		}
	}
	s.Database = strings.TrimPrefix(u.Path, "/")
	if s.Host == "" || s.Database == "" {
		return fmt.Errorf("database url needs host and database name")
	}
	return nil
}

// TenantDatabase is the alias tenant `<schema>_func` clients connect to.
// It maps to the same Postgres database with its own server-connection
// cap (Settings.TenantMaxDBConnections).
func (s Settings) TenantDatabase() string { return s.Database + "_tenant" }

// RenderINI returns pgbouncer.ini.
func RenderINI(s Settings) string {
	var b strings.Builder
	tenantMax := s.TenantMaxDBConnections
	if tenantMax <= 0 {
		tenantMax = s.MaxDBConnections
	}
	wait := s.QueryWaitTimeout
	if wait <= 0 {
		wait = 15
	}
	fmt.Fprintf(&b, "[databases]\n%s = host=%s port=%d dbname=%s max_db_connections=%d\n", s.Database, s.Host, s.Port, s.Database, s.MaxDBConnections)
	fmt.Fprintf(&b, "%s = host=%s port=%d dbname=%s max_db_connections=%d\n\n", s.TenantDatabase(), s.Host, s.Port, s.Database, tenantMax)

	users := make([]string, 0, len(s.PlatformPoolSizes))
	for u := range s.PlatformPoolSizes {
		users = append(users, u)
	}
	sort.Strings(users)
	if len(users) > 0 {
		b.WriteString("[users]\n")
		for _, u := range users {
			fmt.Fprintf(&b, "%s = pool_size=%d\n", u, s.PlatformPoolSizes[u])
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, `[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
unix_socket_dir =
auth_type = scram-sha-256
auth_file = %s
pool_mode = transaction
; the functions pod (user code with network access) can open sockets
; here; cap them well below file-descriptor limits
max_client_conn = 500
default_pool_size = %d
; per-database caps are set on the [databases] entries above
; unauthenticated clients can't hold slots for long
client_login_timeout = 5
; clients queue for a server connection instead of failing
query_wait_timeout = %d
server_idle_timeout = 60
server_lifetime = 1800
; protocol-level prepared statements (pgx cache modes, postgres.js)
max_prepared_statements = 200
; reset every server connection after every transaction, so no session
; state (SET, set_config(..., false), temp tables, LISTEN) reaches the
; next client — the release-time reset in the app can't cover a
; transaction-mode pooler (#641)
server_reset_query = DISCARD ALL
server_reset_query_always = 1
server_tls_sslmode = %s
ignore_startup_parameters = extra_float_digits
log_connections = 0
log_disconnections = 0
stats_period = 60
`, s.AuthFile, s.TenantPoolSize, wait, s.ServerTLS)
	return b.String()
}

// PlatformUser is a platform role PgBouncer logs in as with a plaintext
// password.
type PlatformUser struct {
	User     string
	Password string
}

// PlatformUserFromURL extracts user and password from a postgres:// URL.
func PlatformUserFromURL(databaseURL string) (PlatformUser, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return PlatformUser{}, fmt.Errorf("parse database url: %w", err)
	}
	pw, _ := u.User.Password()
	if u.User.Username() == "" || pw == "" {
		return PlatformUser{}, fmt.Errorf("database url has no user/password")
	}
	return PlatformUser{User: u.User.Username(), Password: pw}, nil
}

// TenantSchemas lists shared-cluster tenant schemas that have a function
// role (the only tenant roles PgBouncer serves).
func TenantSchemas(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT n.nspname
		  FROM pg_namespace n
		  JOIN pg_roles r ON r.rolname = n.nspname || '_func'
		 WHERE n.nspname ~ '^tenant_[0-9a-f_]+$'
		 ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("list tenant schemas: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// RenderUserlist returns the auth_file contents: platform users with
// plaintext passwords, tenant function roles with derived SCRAM verifiers.
func RenderUserlist(platform []PlatformUser, secret []byte, schemas []string) (string, error) {
	seen := map[string]bool{}
	var b strings.Builder
	for _, p := range platform {
		if seen[p.User] {
			continue
		}
		seen[p.User] = true
		fmt.Fprintf(&b, "%s %s\n", quote(p.User), quote(p.Password))
	}
	for _, s := range schemas {
		v, err := tenantlogin.ScramVerifier(secret, s)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s %s\n", quote(tenantlogin.FuncRole(s)), quote(v))
	}
	return b.String(), nil
}

// quote renders a userlist field: double-quoted, inner quotes doubled.
func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// DirectFuncConnections is how many connections a tenant's `<schema>_func`
// role can hold outside the pooler: one cron job (worker) and one console
// dry run (gateway). The functions runner routes through the pooler
// (PR 3) and holds none directly; while a runner rollout still has old
// pods connecting directly, a busy tenant can briefly exceed the limit
// (53300, retryable).
const DirectFuncConnections = 2

// CheckTenantBudget verifies that all pooler replicas together plus the
// direct users of a tenant role fit in the role's CONNECTION LIMIT.
func CheckTenantBudget(s Settings) error {
	replicas := s.Replicas
	if replicas < 1 {
		replicas = 1
	}
	if need := replicas*s.TenantPoolSize + DirectFuncConnections; need > tenantlogin.FuncConnLimit {
		return fmt.Errorf("tenant budget: %d replicas × pool %d + %d direct = %d > FuncConnLimit %d",
			replicas, s.TenantPoolSize, DirectFuncConnections, need, tenantlogin.FuncConnLimit)
	}
	return nil
}
