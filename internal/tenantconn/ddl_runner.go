package tenantconn

// DDLRunner runs platform/console SQL on a shared-cluster tenant's own
// `<schema>_ddl` login instead of eurobase_migrator (step 6d). It mirrors
// the migration executor's connection pattern: a DIRECT connection (not the
// PgBouncer pooler — DDL needs session semantics), as the per-tenant `_ddl`
// role, with the password derived from DDL_PASSWORD_SECRET.
//
// Team / dedicated projects are NOT served here — they keep running console
// SQL as the dedicated instance's owner (there is no `_ddl` role there). The
// caller only hands shared-cluster traffic to this runner.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ddlSchemaRe = regexp.MustCompile(`^tenant_[0-9a-f_]+$`)

// DDLRunner implements query.TenantLoginRunner for the `_ddl` login.
type DDLRunner struct {
	base   *pgx.ConnConfig // a DIRECT (non-pooler) base DSN config, cloned per run
	secret []byte
	// ensure applies the persistent `_ddl` login settings for a schema when a
	// connection is refused (a tenant the keeper hasn't reached yet). Usually
	// tenantlogin.(*DDLEnsurer).EnsureOne.
	ensure func(ctx context.Context, schema string) error
	budget time.Duration
}

// NewDDLRunner builds a runner. base is a direct DATABASE_URL-derived config
// (NOT the pooler URL). Returns nil if base or secret is missing.
func NewDDLRunner(base *pgx.ConnConfig, secret []byte, ensure func(ctx context.Context, schema string) error) *DDLRunner {
	if base == nil || len(secret) == 0 {
		return nil
	}
	return &DDLRunner{base: base, secret: secret, ensure: ensure, budget: 60 * time.Second}
}

// Run opens a short-lived direct connection as `<schema>_ddl`, applies setup
// and runs fn in one transaction. On a login refusal it applies the login
// settings once (ensure) and retries, matching MigrationExecutor.
func (r *DDLRunner) Run(ctx context.Context, schema string, readOnly bool, setup, fn func(context.Context, pgx.Tx) error) error {
	if !ddlSchemaRe.MatchString(schema) {
		return fmt.Errorf("invalid tenant schema %q", schema)
	}
	cctx, cancel := context.WithTimeout(ctx, r.budget)
	defer cancel()

	cfg := r.base.Copy()
	cfg.User = tenantlogin.DDLRole(schema)
	cfg.Password = tenantlogin.DDLPassword(r.secret, schema)

	conn, err := r.connect(cctx, cfg, schema)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		conn.Close(closeCtx) //nolint:errcheck
	}()

	txOpts := pgx.TxOptions{}
	if readOnly {
		txOpts.AccessMode = pgx.ReadOnly
	}
	tx, err := conn.BeginTx(cctx, txOpts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(cctx) //nolint:errcheck

	if _, err := tx.Exec(cctx, "SET LOCAL standard_conforming_strings = on"); err != nil {
		return fmt.Errorf("pin string parsing: %w", err)
	}
	if setup != nil {
		if err := setup(cctx, tx); err != nil {
			return err
		}
	}
	if err := fn(cctx, tx); err != nil {
		return err
	}
	return tx.Commit(cctx)
}

// ddlConnectRetries / ddlConnectBackoff bound the retry on a momentarily
// saturated role (53300). Each consumer connection is short-lived, so a slot
// usually frees within a few hundred ms; the whole retry stays inside the
// run budget.
const (
	ddlConnectRetries = 4
	ddlConnectBackoff = 100 * time.Millisecond
)

// connect opens the `_ddl` connection, recovering from the two expected
// transient failures:
//   - a refused login (28P01/28000) — the worker keeper hasn't reached this
//     tenant yet, or the tenant changed its own password: apply the login
//     once (ensure) and retry;
//   - "too many connections for role" (53300) — DDLConnLimit momentarily
//     saturated by concurrent console/MCP activity: brief bounded backoff.
//
// Everything is bounded by ctx (the run budget). A still-failing connect
// returns ErrConnect (→ 503 at the handler), never a panic or a silent
// fall-through.
func (r *DDLRunner) connect(ctx context.Context, cfg *pgx.ConnConfig, schema string) (*pgx.Conn, error) {
	ensured := false
	for attempt := 0; ; attempt++ {
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err == nil {
			return conn, nil
		}
		switch {
		case isLoginRefused(err) && r.ensure != nil && !ensured:
			ensured = true
			if ensureErr := r.ensure(ctx, schema); ensureErr != nil {
				slog.Error("tenantconn: ensure ddl login", "schema", schema, "error", ensureErr)
				return nil, ErrConnect
			}
			// retry the connect immediately
		case isTooManyConnections(err) && attempt < ddlConnectRetries:
			select {
			case <-ctx.Done():
				slog.Error("tenantconn: connect as ddl role timed out while role saturated", "role", cfg.User)
				return nil, ErrConnect
			case <-time.After(ddlConnectBackoff):
			}
		default:
			slog.Error("tenantconn: connect as ddl role", "role", cfg.User, "error", err)
			return nil, ErrConnect
		}
	}
}

// isLoginRefused: wrong password (28P01) or role may not log in (28000).
func isLoginRefused(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "28P01" || pgErr.Code == "28000")
}

// isTooManyConnections: the role hit its CONNECTION LIMIT (53300).
func isTooManyConnections(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "53300"
}
