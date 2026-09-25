// Package db provides database connection pooling for Eurobase.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Option configures NewPool.
type Option func(*pgxpool.Config)

// WithSessionReset resets every connection's session state when it is
// released back to the pool (ResetSession). Use it for pools that run
// customer SQL or customer code (RPC bodies, triggers): the gateway's
// runtime and developer pools, and Team PoolCache pools. Not for the
// worker pool — River keeps a LISTEN there.
func WithSessionReset() Option {
	return func(c *pgxpool.Config) { c.AfterRelease = ResetSession }
}

// ResetSession is a pgxpool AfterRelease hook (pgx runs it in a goroutine,
// off the request path). It drops all session state a customer statement
// or customer code could have left on the connection — a committed plain
// SET, set_config(…, false), temp tables, LISTEN, prepared statements —
// so it never reaches the next request, which may be another customer's
// (#641). When the connection's exec mode caches statements or
// descriptions, DeallocateAll first clears pgx's caches too. Returning
// false (reset failed) makes the pool destroy the connection.
func ResetSession(conn *pgx.Conn) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch conn.Config().DefaultQueryExecMode {
	case pgx.QueryExecModeCacheStatement, pgx.QueryExecModeCacheDescribe:
		if err := conn.DeallocateAll(ctx); err != nil {
			return false
		}
	}
	_, err := conn.Exec(ctx, "DISCARD ALL")
	return err == nil
}

// NewPool creates a new pgxpool connection pool with sensible defaults.
func NewPool(ctx context.Context, databaseURL string, opts ...Option) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	config.MaxConns = 25
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second

	// Use DescribeExec so that cached prepared statements are automatically
	// re-described when the schema changes (e.g. after DROP/ADD COLUMN).
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	for _, o := range opts {
		o(config)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}
