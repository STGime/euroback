package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Objects a developer creates must apply row-level security for whoever
// uses them. On this platform the owner of a developer's objects also owns
// their tables, and the owner skips the tables' RLS policies, so anything
// that runs with its owner's rights would read past them:
//
//   - a view without `security_invoker = true` (it reads its tables as its
//     owner; `security_invoker` makes it check the reader instead);
//   - a materialized view (it stores its rows; RLS can't apply at all);
//   - a SECURITY DEFINER function or procedure (it runs as its owner);
//   - a rule (its actions run with the rule owner's rights). The `_RETURN`
//     rule that defines a view is the view itself and is covered above.
//
// The rule is checked on the database, not on the SQL text: after the
// developer's SQL has run, in the same transaction, any such object that
// is new or changed by it refuses (and rolls back) the request. That also
// covers objects made by DO blocks, functions or dynamic SQL. Objects that
// were already there unchanged are left alone; they are matched by oid
// and definition (a view's stored query, a function's full definition, a
// rule's actions; a disabled rule doesn't count), so editing or enabling an existing one is refused, while
// renaming it or a column of a table it reads is not.

// guardedObject is one object that runs with its owner's rights.
type guardedObject struct {
	kind string // "view", "materialized", "definer", "rule"
	name string
}

// ownerRightsObjects lists the project's objects that run with their
// owner's rights, keyed by kind, oid and (for functions and rules) a hash
// of the definition. Developer SQL earlier in the transaction may have
// changed search_path (e.g. inside a DO block) and defined operators in
// its own schema, so the query runs with only pg_catalog on the path and
// the previous path is restored afterwards. View options are read in Go.
func ownerRightsObjects(ctx context.Context, tx pgx.Tx, schemaName string) (map[string]guardedObject, error) {
	var prevPath string
	if err := tx.QueryRow(ctx, "SELECT pg_catalog.current_setting('search_path')").Scan(&prevPath); err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog, pg_temp"); err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	objs, err := queryOwnerRightsObjects(ctx, tx, schemaName)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_catalog.set_config('search_path', $1, true)", prevPath); err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	return objs, nil
}

func queryOwnerRightsObjects(ctx context.Context, tx pgx.Tx, schemaName string) (map[string]guardedObject, error) {
	rows, err := tx.Query(ctx, `
		WITH ns AS (SELECT n.oid FROM pg_catalog.pg_namespace n WHERE n.nspname = $1)
		SELECT CASE c.relkind WHEN 'm' THEN 'materialized' ELSE 'view' END,
		       c.oid::text || ':' || pg_catalog.md5(coalesce((
		           SELECT r.ev_action::text FROM pg_catalog.pg_rewrite r
		            WHERE r.ev_class = c.oid AND r.rulename = '_RETURN'), '')),
		       c.relname, coalesce(c.reloptions, '{}'::text[])
		  FROM pg_catalog.pg_class c
		 WHERE c.relnamespace = (SELECT oid FROM ns) AND c.relkind IN ('v', 'm')
		UNION ALL
		SELECT 'definer', p.oid::text || ':' || pg_catalog.md5(pg_catalog.pg_get_functiondef(p.oid)),
		       p.proname, '{}'::text[]
		  FROM pg_catalog.pg_proc p
		 WHERE p.pronamespace = (SELECT oid FROM ns) AND p.prosecdef AND p.prokind IN ('f', 'p')
		UNION ALL
		SELECT 'rule', r.oid::text || ':' || pg_catalog.md5(r.ev_action::text || coalesce(r.ev_qual::text, '')
		           || r.ev_type::text || r.is_instead::text),
		       r.rulename || ' on ' || c.relname, '{}'::text[]
		  FROM pg_catalog.pg_rewrite r
		  JOIN pg_catalog.pg_class c ON c.oid = r.ev_class
		 WHERE c.relnamespace = (SELECT oid FROM ns) AND r.rulename <> '_RETURN'
		   AND r.ev_enabled <> 'D'`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	defer rows.Close()
	out := map[string]guardedObject{}
	for rows.Next() {
		var kind, key, name string
		var options []string
		if err := rows.Scan(&kind, &key, &name, &options); err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}
		if kind == "view" && securityInvokerOn(options) {
			continue
		}
		out[kind+":"+key] = guardedObject{kind: kind, name: name}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	return out, nil
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

// withOwnerRightsGuard runs fn and then refuses the request if fn left an
// object in schemaName that runs with its owner's rights and wasn't there
// (unchanged) before. fn's error is returned unchanged.
func withOwnerRightsGuard(ctx context.Context, tx pgx.Tx, schemaName string, fn func() error) error {
	before, err := ownerRightsObjects(ctx, tx, schemaName)
	if err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	after, err := ownerRightsObjects(ctx, tx, schemaName)
	if err != nil {
		return err
	}
	found := map[string][]string{}
	for key, o := range after {
		if _, existed := before[key]; !existed {
			found[o.kind] = append(found[o.kind], o.name)
		}
	}
	for _, names := range found {
		sort.Strings(names)
	}
	switch {
	case len(found["definer"]) > 0:
		return refusedf("function %s can't be SECURITY DEFINER: it would run with the owner's rights and skip the row-level security "+
			"of your tables. Use a plain function (SECURITY INVOKER) with RLS policies, or server-side code with the secret key",
			quoteList(found["definer"]))
	case len(found["rule"]) > 0:
		return refusedf("rule %s is not supported: a rule's actions run with the owner's rights and skip the row-level security "+
			"of your tables. Use a trigger instead", quoteList(found["rule"]))
	case len(found["materialized"]) > 0:
		return refusedf("materialized view %s is not supported: a materialized view can't apply row-level security, "+
			"so anyone who can read it sees all of its rows. Use a view WITH (security_invoker = true), "+
			"or a table you refresh from a cron job", quoteList(found["materialized"]))
	case len(found["view"]) > 0:
		return refusedf("view %s needs security_invoker = true, e.g. CREATE OR REPLACE VIEW %s WITH (security_invoker = true) AS SELECT …. "+
			"Repeat the WITH clause whenever you replace or re-create a view: CREATE OR REPLACE VIEW without it switches the option off. "+
			"Without it, reading the view skips the row-level security of the tables it reads",
			quoteList(found["view"]), pgx.Identifier{found["view"][0]}.Sanitize())
	}
	return nil
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = `"` + strings.ReplaceAll(n, `"`, `""`) + `"`
	}
	return strings.Join(q, ", ")
}
