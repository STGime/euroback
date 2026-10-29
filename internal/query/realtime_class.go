package query

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TableIsPublicReadable reports whether every reader — including an
// anonymous (public-key) caller — can SELECT every row of the table
// under RLS. The realtime fan-out uses it (via a resolver injected into
// the event publisher, #861) to decide whether an owner-less row may be
// delivered to end-user and anonymous subscribers, or only to the
// service role (#860's fail-closed default).
//
// It is deliberately conservative: a table counts as public ONLY when
//
//   - row-level security is disabled on it, so RLS imposes no filter; OR
//   - there is a PERMISSIVE policy covering SELECT (cmd SELECT or ALL)
//     whose USING qualifier is literally `true` — every row passes it —
//     AND no RESTRICTIVE policy covering SELECT narrows that (restrictive
//     policies AND-combine, so one with a non-trivial qual means some
//     rows are hidden).
//
// Anything else — an owner/tenant-scoped qual, an authenticated-only
// read, a semantically-but-not-literally-true qual, a missing table, or
// any error — returns false. The caller treats false as "scoped": the
// owner filter applies and owner-less rows reach only the service role.
// Mis-classifying a scoped table as public would re-open the cross-account
// delivery #860 closed, so when in doubt this says not-public.
//
// pg_class.relforcerowsecurity is not consulted: FORCE RLS only makes the
// table OWNER subject to RLS, and realtime never reads as the owner — the
// gateway / <schema>_func roles are non-owners, for whom ordinary RLS
// applies whether or not FORCE is set.
func TableIsPublicReadable(ctx context.Context, pool *pgxpool.Pool, schema, table string) (bool, error) {
	const q = `
SELECT
    NOT c.relrowsecurity
    OR (
        EXISTS (
            SELECT 1 FROM pg_catalog.pg_policy p
             WHERE p.polrelid = c.oid
               AND p.polpermissive
               AND p.polcmd IN ('r', '*')               -- SELECT or ALL
               AND pg_catalog.pg_get_expr(p.polqual, p.polrelid) = 'true'
        )
        AND NOT EXISTS (
            SELECT 1 FROM pg_catalog.pg_policy p
             WHERE p.polrelid = c.oid
               AND NOT p.polpermissive                   -- RESTRICTIVE
               AND p.polcmd IN ('r', '*')
               AND COALESCE(pg_catalog.pg_get_expr(p.polqual, p.polrelid), 'true') <> 'true'
        )
    )
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'r'`

	var public bool
	err := pool.QueryRow(ctx, q, schema, table).Scan(&public)
	if err != nil {
		if err == pgx.ErrNoRows {
			// No such table (dropped, or a view/other relkind): nothing to
			// deliver publicly. Fail closed.
			return false, nil
		}
		return false, fmt.Errorf("resolve realtime table class %s.%s: %w", schema, table, err)
	}
	return public, nil
}
