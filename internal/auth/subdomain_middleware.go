package auth

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SubdomainMiddleware extracts the project slug from the Host header
// (e.g. "my-app.eurobase.app" → "my-app") and resolves the project context.
// This allows SDK requests to reach projects via their subdomain URL
// without needing an API key for project identification — though an API key
// is still required for authentication.
type SubdomainMiddleware struct {
	pool   *pgxpool.Pool
	suffix string // e.g. ".eurobase.app"
	// lastActiveBump coalesces the per-request `last_active_at`
	// UPDATE. Without this, a busy project at 10 req/s would fire
	// 10 UPDATEs/s on the same `projects` row → hot tuple, WAL /
	// vacuum pressure. The idle-pause cron runs hourly and only
	// needs coarse-grained timestamps, so a 60-s coalescing window
	// drops the write load by ~600× on hot projects with no loss
	// of pause-detection accuracy. (#284 review high #1.)
	//
	// Key: project ID (string). Value: unix nanos of the last bump.
	lastActiveBump sync.Map
}

// lastActiveBumpInterval is the coalescing window for last_active_at
// writes. The idle-pause cron has 30-day granularity so anything
// finer than a minute is DB write waste.
const lastActiveBumpInterval = 60 * time.Second

// NewSubdomainMiddleware creates middleware that resolves projects by subdomain.
// suffix is the domain suffix to strip (e.g. ".eurobase.app").
func NewSubdomainMiddleware(pool *pgxpool.Pool, suffix string) *SubdomainMiddleware {
	if !strings.HasPrefix(suffix, ".") {
		suffix = "." + suffix
	}
	return &SubdomainMiddleware{pool: pool, suffix: suffix}
}

// wakeSleepBase + wakeSleepJitter make up the artificial ~30 s pause
// applied on the FIRST request that wakes a paused project. Public-
// beta launch plan decision #5: the real DB flip is ~200 ms, but we
// deliberately hold the response for ~30 s so "Pro never pauses"
// becomes a visible pain point on Free rather than an invisible one.
// Subsequent requests pass through instantly.
const (
	wakeSleepBase   = 28 * time.Second
	wakeSleepJitter = 4 * time.Second // 0..4s → total 28-32s
)

// Handler is the chi-compatible middleware func.
// It extracts the slug from the Host header, resolves the project,
// and injects the ProjectContext. The API key middleware still runs
// after this to authenticate the request — this middleware only narrows
// which project the request targets.
//
// It also runs the idle-pause lifecycle for the resolved project:
//   - If the project's state = 'paused', flip it back to 'active'
//     (a DB write) and sleep ~30 s before letting the request
//     through. The sleep is the intentional "you got paused"
//     conversion signal.
//   - On any request that passes through, bump `last_active_at =
//     now()` so the idle-pause cron reads a fresh timestamp on its
//     next tick. This runs in a goroutine so it doesn't add latency
//     to the response.
func (m *SubdomainMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// Strip port if present (e.g. "my-app.eurobase.app:8080").
		if idx := strings.LastIndex(host, ":"); idx != -1 {
			host = host[:idx]
		}

		if !strings.HasSuffix(host, m.suffix) {
			// Not a subdomain request — pass through (handled by other routes).
			next.ServeHTTP(w, r)
			return
		}

		slug := strings.TrimSuffix(host, m.suffix)
		if slug == "" || slug == "api" || slug == "console" {
			// Reserved subdomains — pass through.
			next.ServeHTTP(w, r)
			return
		}

		pc := ProjectContext{Slug: slug}
		var state string
		err := m.pool.QueryRow(r.Context(),
			`SELECT id, schema_name, jwt_secret, auth_config, state
			 FROM projects
			 WHERE slug = $1 AND status = 'active'`,
			slug,
		).Scan(&pc.ProjectID, &pc.SchemaName, &pc.JWTSecret, &pc.AuthConfig, &state)
		if err != nil {
			slog.Warn("subdomain project not found", "slug", slug, "error", err)
			http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
			return
		}

		// Idle-pause lifecycle. If paused, flip the state row back to
		// 'active' + hold the response for ~30 s. See the const block
		// above for the reasoning. Only fires on the state-flipping
		// request; subsequent requests skip both branches.
		if state == "paused" {
			// Detached ctx so a client bailing mid-wake doesn't cancel
			// the UPDATE — the write is idempotent + must complete
			// even if this request abandons (otherwise the project
			// stays paused and every subsequent request repeats the
			// wake).  (#284 review med #7.)
			wakeCtx, wakeCancel := timeoutCtx(5 * time.Second)
			tag, err := m.pool.Exec(wakeCtx,
				`UPDATE public.projects
				    SET state = 'active', last_active_at = now()
				  WHERE id = $1 AND state = 'paused'`,
				pc.ProjectID,
			)
			wakeCancel()
			if err != nil {
				slog.Error("wake paused project", "slug", slug, "project_id", pc.ProjectID, "error", err)
				http.Error(w, `{"error":"wake failed — please retry"}`, http.StatusServiceUnavailable)
				return
			}
			// Concurrent-wake safety: two requests can race the SELECT
			// above and both attempt the guarded UPDATE. The
			// `WHERE state='paused'` clause means only one row flips;
			// the loser gets `RowsAffected() == 0`. Without this
			// check, the loser would ALSO sleep 30 s + set the
			// wake header, double-billing the pause pain for one
			// actual pause. Skip both when we didn't do the flip.
			// (#284 review high #3.)
			if tag.RowsAffected() > 0 {
				jitter := time.Duration(rand.Int64N(int64(wakeSleepJitter))) //nolint:gosec // not security-sensitive
				sleep := wakeSleepBase + jitter
				slog.Info("waking paused project", "slug", slug, "project_id", pc.ProjectID, "sleep", sleep)
				// Signal to any client-side handler that the delay was
				// intentional. Console renders the upgrade prompt on
				// seeing this header.
				w.Header().Set("X-Eurobase-Woke-From-Pause", "true")
				select {
				case <-time.After(sleep):
				case <-r.Context().Done():
					// Client bailed mid-wake; abandon.
					return
				}
			}
		} else {
			m.maybeBumpLastActive(pc.ProjectID)
		}

		slog.Debug("subdomain resolved", "slug", slug, "project_id", pc.ProjectID)

		// Inject the project context. The API key middleware will still
		// validate the apikey header and set KeyType.
		ctx := ContextWithProject(r.Context(), &pc)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// timeoutCtx returns a background context with the given timeout. Used
// for fire-and-forget writes that must not outlive their intent.
func timeoutCtx(d time.Duration) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// maybeBumpLastActive fires the last_active_at UPDATE for `projectID`
// iff `shouldBump` returns true. On projects taking 10+ req/s this
// eliminates ~99% of what would otherwise be a hot-tuple write
// pattern (#284 review high #1).
//
// last-write-wins races between two concurrent goroutines are fine —
// the UPDATE is idempotent, and the coarse timestamps are only
// consumed by the idle-pause cron with 30-day granularity.
func (m *SubdomainMiddleware) maybeBumpLastActive(projectID string) {
	if !shouldBump(&m.lastActiveBump, projectID, time.Now(), lastActiveBumpInterval) {
		return
	}
	go func(projectID string) {
		ctx, cancel := timeoutCtx(2 * time.Second)
		defer cancel()
		if _, err := m.pool.Exec(ctx,
			`UPDATE public.projects
			    SET last_active_at = now()
			  WHERE id = $1`,
			projectID,
		); err != nil {
			slog.Debug("bump last_active_at failed", "project_id", projectID, "error", err)
		}
	}(projectID)
}

// shouldBump is the pure coalescing decision. Extracted from
// maybeBumpLastActive so the guard is directly unit-testable and the
// test doesn't have to keep a shadow copy in sync (#284 round-2
// review low #1).
func shouldBump(store *sync.Map, key string, now time.Time, interval time.Duration) bool {
	nowNanos := now.UnixNano()
	if prev, ok := store.Load(key); ok {
		if nowNanos-prev.(int64) < int64(interval) {
			return false
		}
	}
	store.Store(key, nowNanos)
	return true
}
