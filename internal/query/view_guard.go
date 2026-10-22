package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Views a developer creates must apply row-level security for whoever
// reads them. A plain view runs with its owner's privileges, and on this
// platform the owner of a developer's views is also the owner of their
// tables — so reading such a view would skip the tables' RLS policies.
// `security_invoker = true` makes the view check the reader instead.
// Materialized views store their rows and can't apply RLS at all.
//
// The rule is checked on the database, not on the SQL text: after the
// developer's SQL has run, in the same transaction, any view in the
// project's schema that is new or changed by it and runs as its owner, or
// any new materialized view, refuses (and rolls back) the request. That
// also covers views made by DO blocks, functions or dynamic SQL. Views
// that already ran as their owner before the request are left alone, so
// existing projects keep working until they are converted.

// ownerRunViews lists the project's views that run as their owner, plus
// all materialized views: oid → description. Developer SQL earlier in the
// transaction may have changed search_path (e.g. inside a DO block) and
// defined operators in its own schema, so the query runs with only
// pg_catalog on the path and the previous path is restored afterwards.
// The options are read in Go.
func ownerRunViews(ctx context.Context, tx pgx.Tx, schemaName string) (map[uint32]ownerRunView, error) {
	var prevPath string
	if err := tx.QueryRow(ctx, "SELECT pg_catalog.current_setting('search_path')").Scan(&prevPath); err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog, pg_temp"); err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	views, err := queryOwnerRunViews(ctx, tx, schemaName)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_catalog.set_config('search_path', $1, true)", prevPath); err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	return views, nil
}

func queryOwnerRunViews(ctx context.Context, tx pgx.Tx, schemaName string) (map[uint32]ownerRunView, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.oid, c.relname, c.relkind::text, coalesce(c.reloptions, '{}'::text[])
		  FROM pg_catalog.pg_class c
		 WHERE c.relnamespace = (SELECT n.oid FROM pg_catalog.pg_namespace n WHERE n.nspname = $1)
		   AND c.relkind IN ('v', 'm')`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	defer rows.Close()
	out := map[uint32]ownerRunView{}
	for rows.Next() {
		var (
			oid     uint32
			name    string
			kind    string
			options []string
		)
		if err := rows.Scan(&oid, &name, &kind, &options); err != nil {
			return nil, fmt.Errorf("list views: %w", err)
		}
		v := ownerRunView{name: name, materialized: kind == "m"}
		if v.materialized || !securityInvokerOn(options) {
			out[oid] = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	return out, nil
}

type ownerRunView struct {
	name         string
	materialized bool
}

// securityInvokerOn reports whether a view's reloptions turn
// security_invoker on, accepting every spelling PostgreSQL's boolean
// parser accepts for true (true/yes and their prefixes, on, 1).
func securityInvokerOn(options []string) bool {
	on := false
	for _, o := range options {
		k, v, ok := strings.Cut(o, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "security_invoker") {
			continue
		}
		v = strings.ToLower(strings.TrimSpace(v))
		on = v == "on" || v == "1" ||
			(v != "" && strings.HasPrefix("true", v)) ||
			(v != "" && strings.HasPrefix("yes", v))
	}
	return on
}

// withViewGuard runs fn and then refuses the request if fn left a view in
// schemaName that runs as its owner and wasn't there before, or a new
// materialized view. fn's error is returned unchanged.
func withViewGuard(ctx context.Context, tx pgx.Tx, schemaName string, fn func() error) error {
	before, err := ownerRunViews(ctx, tx, schemaName)
	if err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	after, err := ownerRunViews(ctx, tx, schemaName)
	if err != nil {
		return err
	}
	var views, matviews []string
	for oid, v := range after {
		if _, existed := before[oid]; existed {
			continue
		}
		if v.materialized {
			matviews = append(matviews, v.name)
		} else {
			views = append(views, v.name)
		}
	}
	sort.Strings(views)
	sort.Strings(matviews)
	switch {
	case len(matviews) > 0:
		return refusedf("materialized view %s is not supported: a materialized view can't apply row-level security, "+
			"so anyone who can read it sees all of its rows. Use a view WITH (security_invoker = true), "+
			"or a table you refresh from a cron job", quoteList(matviews))
	case len(views) > 0:
		return refusedf("view %s needs security_invoker = true, e.g. CREATE VIEW %s WITH (security_invoker = true) AS SELECT …. "+
			"Without it, reading the view skips the row-level security of the tables it reads",
			quoteList(views), pgx.Identifier{views[0]}.Sanitize())
	}
	return nil
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = `"` + n + `"`
	}
	return strings.Join(q, ", ")
}
