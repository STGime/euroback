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

	mu        sync.Mutex
	pools     map[string]*cachedPool
	stop      chan struct{}
	closeOnce sync.Once
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

// Close closes every cached pool and stops the sweeper. Idempotent
// — sync.Once guards the stop-channel close so ambient shutdown
// paths (test cleanup, ctx-cancel handlers, signal handlers) don't
// race into a panic on a second call.
func (c *PoolCache) Close() {
	c.closeOnce.Do(func() {
		close(c.stop)
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, cp := range c.pools {
		cp.pool.Close()
		delete(c.pools, id)
	}
}

// ErrRuntimeCredMissing is returned by Get when the project's
// project_databases row has NULL runtime_* columns. This is the
// pre-bootstrap window on a fresh Team-tier provisioning, a
// bootstrap that failed and wasn't backfilled, or an environment
// with RUNTIME_PASSWORD_SECRET unset (worker skips the bootstrap
// step). Callers MUST NOT fall back to the owner credential
// silently — that would connect SDK end-user traffic as the table
// owner, which bypasses RLS on every owned tenant table and lets
// every end-user of the app read every other end-user's rows.
// The right behaviour on this error is to log loudly and return
// nil from the resolver so the request falls back to the shared
// pool (visible failure: 42P01) instead of silently leaking rows.
var ErrRuntimeCredMissing = errors.New("dbprovider: runtime credential not populated on project_databases row")

// Get returns the RUNTIME (non-owner) pool for projectID. This is
// the SDK-facing pool — connects as eurobase_gateway so RLS
// policies bind for end-user traffic.
//
// Returns:
//   - (nil, pgx.ErrNoRows) if the project has no live
//     project_databases row — caller falls back to shared pool.
//   - (nil, ErrRuntimeCredMissing) if the row exists but runtime_*
//     is NULL — caller MUST refuse to route (falling back to shared
//     is the right posture; do not attempt owner as a substitute).
//   - (pool, nil) on success.
func (c *PoolCache) Get(ctx context.Context, projectID string) (*pgxpool.Pool, error) {
	return c.get(ctx, projectID, false)
}

// GetOwner returns the OWNER pool for projectID — connects as
// eurobase_owner. Used by console-authenticated DDL / DML on Team-
// tier so tables created via the console SQL editor are owned by
// the owner (not by the runtime role) — that preserves the shared-
// cluster invariant that SDK runtime traffic (as eurobase_gateway,
// non-owner) has RLS enforced against owner-created tables.
//
// SDK / end-user traffic must NEVER call this — see the loud
// warning in router.go above the poolResolver.
//
// Cached separately from the runtime pool (same idle-TTL, same
// concurrent-open guard, same Evict semantics).
func (c *PoolCache) GetOwner(ctx context.Context, projectID string) (*pgxpool.Pool, error) {
	return c.get(ctx, projectID, true)
}

// get is the shared body of Get / GetOwner. `ownerMode=true` picks
// the owner credential; otherwise the runtime credential (falling
// back to owner via EffectiveCredential).
//
// Cache keys are the projectID (runtime) or projectID+":owner"
// (owner) so the two variants coexist without collision.
func (c *PoolCache) get(ctx context.Context, projectID string, ownerMode bool) (*pgxpool.Pool, error) {
	key := projectID
	if ownerMode {
		key = projectID + ":owner"
	}
	c.mu.Lock()
	if cp, ok := c.pools[key]; ok {
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

	// Race guard: between the worker's InsertProvisioning (state
	// transitions to 'provisioning', host/port not yet known) and
	// MarkActive (host/port populated from Scaleway's Describe),
	// GetLiveByProject returns a row with Host="" / Port=0. Building
	// a DSN out of that produces `postgres://user:pw@:0/db` and
	// pgxpool.ParseConfig fails with "invalid port (outside range)".
	// Treat this as "no live pool yet" so callers fall back to the
	// shared pool exactly like the pre-InsertProvisioning ErrNoRows
	// case — no functional difference to the caller, one less
	// spurious ERROR line in the logs.
	if rec.Host == "" || rec.Port == 0 {
		return nil, pgx.ErrNoRows
	}

	// Credential selection:
	//   ownerMode=false → RUNTIME (non-owner). Errors with
	//     ErrRuntimeCredMissing when the row's runtime slot is
	//     NULL — we deliberately do NOT fall back to the owner
	//     credential here. The old EffectiveCredential fallback
	//     was safe when the pool was consumed only by usage
	//     tracker / bootstrap paths where owner-connection was
	//     desired; once SDK end-user traffic routes through this
	//     pool (post-TEAM_TIER_ROUTING flip), an owner connection
	//     bypasses RLS on every owned tenant table and every
	//     end-user of the app sees every other end-user's rows.
	//     A missing runtime cred is a bootstrap-hasn't-completed
	//     signal; caller must refuse to route and let the request
	//     fall back to the shared pool (loud 42P01 > silent leak).
	//   ownerMode=true  → OWNER — used by console DDL and
	//     platform-authenticated reads. RLS bypass is intentional
	//     (console is already authorized as admin via RequireRole).
	var username string
	var ct, nonce []byte
	var ver int16
	if ownerMode {
		username = rec.Username
		ct = rec.PasswordCiphertext
		nonce = rec.PasswordNonce
		ver = rec.PasswordKeyVersion
	} else {
		if !rec.allRuntimeSet() {
			return nil, ErrRuntimeCredMissing
		}
		username, ct, nonce, ver = rec.EffectiveCredential()
	}
	password, err := c.cipher.Open(ct, nonce, ver)
	if err != nil {
		return nil, fmt.Errorf("pool cache: decrypt: %w", err)
	}

	dsn := buildDSN(username, password, rec.Host, rec.Port, rec.DatabaseName)
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
	if existing, ok := c.pools[key]; ok {
		pool.Close()
		existing.lastUsed = time.Now()
		p := existing.pool
		c.mu.Unlock()
		return p, nil
	}
	c.pools[key] = &cachedPool{pool: pool, host: rec.Host, lastUsed: time.Now()}
	c.mu.Unlock()

	slog.Info("dedicated pool opened", "project_id", projectID, "host", rec.Host, "owner_mode", ownerMode)
	return pool, nil
}

// Evict closes and removes the cached pools for projectID (both
// runtime and owner variants). Restore cutover calls this so the
// next request opens against the new host. Safe to call for a
// project that isn't cached.
func (c *PoolCache) Evict(projectID string) {
	for _, key := range []string{projectID, projectID + ":owner"} {
		c.mu.Lock()
		cp, ok := c.pools[key]
		if !ok {
			c.mu.Unlock()
			continue
		}
		delete(c.pools, key)
		c.mu.Unlock()
		cp.pool.Close()
		slog.Info("dedicated pool evicted", "project_id", projectID, "key", key, "host", cp.host)
	}
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

// BuildOwnerDSN is the exported alias workers use to build an owner
// connection string for bootstrap. Same shape as the pool cache's
// internal buildDSN; a separate export keeps the internal helper
// package-private without duplicating the URL-encoding logic.
func BuildOwnerDSN(user, password, host string, port int, db string) string {
	return buildDSN(user, password, host, port, db)
}
