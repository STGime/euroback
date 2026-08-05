package dbprovider

// Per-project pgxpool cache — Team-tier M2.5 routing.
//
// A Team / Legal-Team project has its own managed-PG instance
// (M1 provisioned it; M2 populated `project_databases.host`). This
// cache lazily opens a pgxpool against that instance the first time
// the gateway resolves the project and reuses it thereafter.
//
// Free / Pro projects have HasDedicatedDB=false and never reach this
// cache — resolvePool falls back to the shared cluster pool.
//
// Two lifecycle events matter beyond lazy open:
//
//   * Idle eviction (idleTTL): a pool that hasn't served a request
//     within the TTL is closed and removed. Prevents unbounded
//     growth when a project stops serving traffic — the next
//     request just reopens.
//
//   * Explicit eviction (Evict): the restore-cutover path (M3)
//     swaps project_databases.host under the project's feet.
//     Without eviction, cached connections keep pointing at the
//     old instance until idle-TTL. Evict wipes the entry so the
//     next request opens against the new host.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolCache maintains one pgxpool per project (keyed on ProjectID).
// Safe for concurrent use.
type PoolCache struct {
	cipher  *Cipher
	repo    *Repo
	idleTTL time.Duration
	maxConn int32

	mu    sync.Mutex
	pools map[string]*cachedPool
	stop  chan struct{}
}

type cachedPool struct {
	pool     *pgxpool.Pool
	host     string
	lastUsed time.Time
}

// NewPoolCache constructs a cache. idleTTL bounds cache size —
// entries idle longer than the TTL are swept by a background
// goroutine. maxConn caps connections per dedicated pool (default
// 8; small because a Team project's dedicated instance is single-
// tenant and does not need the shared cluster's fan-out).
func NewPoolCache(cipher *Cipher, repo *Repo, idleTTL time.Duration, maxConn int32) *PoolCache {
	if idleTTL <= 0 {
		idleTTL = 30 * time.Minute
	}
	if maxConn <= 0 {
		maxConn = 8
	}
	c := &PoolCache{
		cipher:  cipher,
		repo:    repo,
		idleTTL: idleTTL,
		maxConn: maxConn,
		pools:   make(map[string]*cachedPool),
		stop:    make(chan struct{}),
	}
	go c.sweepLoop()
	return c
}

// Close closes every cached pool and stops the sweeper.
func (c *PoolCache) Close() {
	close(c.stop)
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, cp := range c.pools {
		cp.pool.Close()
		delete(c.pools, id)
	}
}

// Get returns the pool for projectID, opening it on the first call.
// Returns (nil, pgx.ErrNoRows) if the project has no live
// project_databases row — the caller should fall back to the
// shared pool.
func (c *PoolCache) Get(ctx context.Context, projectID string) (*pgxpool.Pool, error) {
	c.mu.Lock()
	if cp, ok := c.pools[projectID]; ok {
		cp.lastUsed = time.Now()
		p := cp.pool
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Unlock()

	// Look up the live row — repo already prefers 'active' over
	// 'provisioning'/'restoring' (fixed in M4 review).
	rec, err := c.repo.GetLiveByProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("pool cache: lookup: %w", err)
	}

	password, err := c.cipher.Open(rec.PasswordCiphertext, rec.PasswordNonce, rec.PasswordKeyVersion)
	if err != nil {
		return nil, fmt.Errorf("pool cache: decrypt: %w", err)
	}

	dsn := buildDSN(rec.Username, password, rec.Host, rec.Port, rec.DatabaseName)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pool cache: parse dsn: %w", err)
	}
	cfg.MaxConns = c.maxConn
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pool cache: open pool: %w", err)
	}

	c.mu.Lock()
	// Concurrent-open guard: if another goroutine won the race,
	// prefer its pool and close ours. Otherwise install.
	if existing, ok := c.pools[projectID]; ok {
		pool.Close()
		existing.lastUsed = time.Now()
		p := existing.pool
		c.mu.Unlock()
		return p, nil
	}
	c.pools[projectID] = &cachedPool{pool: pool, host: rec.Host, lastUsed: time.Now()}
	c.mu.Unlock()

	slog.Info("dedicated pool opened", "project_id", projectID, "host", rec.Host)
	return pool, nil
}

// Evict closes and removes the cached pool for projectID, if any.
// Restore cutover calls this so the next request opens against the
// new host. Safe to call for a project that isn't cached.
func (c *PoolCache) Evict(projectID string) {
	c.mu.Lock()
	cp, ok := c.pools[projectID]
	if !ok {
		c.mu.Unlock()
		return
	}
	delete(c.pools, projectID)
	c.mu.Unlock()
	cp.pool.Close()
	slog.Info("dedicated pool evicted", "project_id", projectID, "host", cp.host)
}

// Size reports the number of currently-cached pools. For metrics /
// tests.
func (c *PoolCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pools)
}

func (c *PoolCache) sweepLoop() {
	ticker := time.NewTicker(c.idleTTL / 4)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.sweep()
		}
	}
}

func (c *PoolCache) sweep() {
	cutoff := time.Now().Add(-c.idleTTL)
	c.mu.Lock()
	var closed []string
	for id, cp := range c.pools {
		if cp.lastUsed.Before(cutoff) {
			cp.pool.Close()
			delete(c.pools, id)
			closed = append(closed, id)
		}
	}
	c.mu.Unlock()
	for _, id := range closed {
		slog.Info("dedicated pool idle-swept", "project_id", id)
	}
}

// buildDSN mirrors the URL shape used by connection_handlers.go
// (Team-tier direct URL). sslmode=require because every managed-PG
// provider needs TLS.
func buildDSN(user, password, host string, port int, db string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/" + db,
	}
	q := u.Query()
	q.Set("sslmode", "require")
	u.RawQuery = q.Encode()
	return u.String()
}
