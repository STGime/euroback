package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/realtime"
	"github.com/jackc/pgx/v5/pgxpool"
)

// realtimePublicCacheTTL bounds how long a table's public-read class is
// cached. A policy change made through raw SQL (CREATE/ALTER/DROP POLICY,
// ALTER TABLE … ROW LEVEL SECURITY) doesn't notify this cache, so a table
// that was public and is then locked down keeps being treated as public
// for at most this long — the one window where realtime could over-deliver
// a now-scoped table. Kept short for that reason. (Endpoint-driven
// invalidation on the DDL/preset paths is the follow-up in #861.)
const realtimePublicCacheTTL = 10 * time.Second

// newRealtimePublicResolver builds the resolver the realtime publisher
// uses to decide whether a table's owner-less rows may be broadcast to
// every subscriber (#861). It reads the table's RLS class from the
// catalog (query.TableIsPublicReadable) and caches the result per schema
// and table for a short TTL. It fails closed — not public — whenever the
// schema or tenant pool isn't on the request context or the catalog read
// errors, so it can only ever widen delivery for a table it has positively
// confirmed is public-readable.
//
// The schema and the pool come from the request context the write ran on:
// SchemaFromContext, and TenantPoolFromContext for a Team project's
// dedicated instance (shared fallback otherwise). So a Team table is
// classified against its own database; a table whose context carries no
// pool or schema (e.g. the shared fallback isn't set) stays scoped.
func newRealtimePublicResolver(shared *pgxpool.Pool) realtime.PublicTableResolver {
	c := &realtimePublicCache{shared: shared, entries: map[string]publicEntry{}}
	return c.resolve
}

type publicEntry struct {
	public bool
	at     time.Time
}

type realtimePublicCache struct {
	shared *pgxpool.Pool
	mu     sync.Mutex
	// entries keyed by schema + "\x00" + table. The schema is unique per
	// project, so this also separates projects without the project id.
	entries map[string]publicEntry
}

// resolve matches realtime.PublicTableResolver. The project id the
// publisher passes is ignored — the schema and pool come from the
// request context the write ran on (see newRealtimePublicResolver).
func (c *realtimePublicCache) resolve(ctx context.Context, _ string, table string) bool {
	schema := query.SchemaFromContext(ctx)
	if schema == "" || table == "" {
		return false
	}
	pool := query.TenantPoolFromContext(ctx)
	if pool == nil {
		pool = c.shared
	}
	if pool == nil {
		return false
	}

	key := schema + "\x00" + table
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Sub(e.at) < realtimePublicCacheTTL {
		c.mu.Unlock()
		return e.public
	}
	c.mu.Unlock()

	// Bounded so a slow catalog read can't hold the write path; failure
	// (including context cancellation) falls through to not-public.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	public, err := query.TableIsPublicReadable(rctx, pool, schema, table)
	if err != nil {
		public = false
	}

	c.mu.Lock()
	if len(c.entries) >= 50_000 {
		for k, e := range c.entries {
			if now.Sub(e.at) > 4*realtimePublicCacheTTL {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = publicEntry{public: public, at: now}
	c.mu.Unlock()
	return public
}
