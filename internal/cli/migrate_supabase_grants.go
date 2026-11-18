package cli

// Supabase apps can protect data with GRANT / REVOKE on top of RLS:
// column-level security (`REVOKE SELECT ON profiles FROM anon;
// GRANT SELECT (id, name) ON profiles TO anon`), tables only the
// service role can write, functions anon can't call. Eurobase has no
// per-role grants — every API request runs as the same database role,
// which can use every table, column and function of the project, and
// only RLS decides. So those restrictions don't carry over, and the
// schema dump (--no-privileges) wouldn't show them. readNarrowedGrants
// lists them from the source so `schema` and `assess` can say so.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// narrowedGrant is one object anon / authenticated can't fully use on
// Supabase.
type narrowedGrant struct {
	object string // "table profiles", "function is_admin(uuid)"
	role   string
	detail string // "no SELECT, UPDATE; column grants: SELECT (id, name)"
}

func (g narrowedGrant) String() string {
	return fmt.Sprintf("%s: %s has %s", g.object, g.role, g.detail)
}

// readNarrowedGrants returns the public tables, views and functions on
// which Supabase's anon or authenticated role lacks a privilege. Objects
// that belong to an extension, and tables with RLS on and no policies
// (closed to API callers either way), are skipped. Returns nothing when the
// roles don't exist (not a Supabase database).
func readNarrowedGrants(ctx context.Context, conn *pgx.Conn) ([]narrowedGrant, error) {
	var out []narrowedGrant
	rows, err := conn.Query(ctx, `
		WITH roles AS (
		  SELECT oid, rolname FROM pg_roles WHERE rolname IN ('anon', 'authenticated')
		), rels AS (
		  SELECT c.oid, c.relname, c.relkind
		    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		   WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		     -- RLS on and no policies: closed to API callers on Eurobase
		     -- too, so missing grants change nothing there.
		     AND NOT (c.relrowsecurity AND NOT EXISTS (SELECT 1 FROM pg_policy pol WHERE pol.polrelid = c.oid))
		     AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                      WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
		)
		SELECT CASE WHEN t.relkind IN ('v', 'm') THEN 'view ' ELSE 'table ' END || t.relname,
		       r.rolname,
		       ARRAY(SELECT p FROM unnest(ARRAY['SELECT', 'INSERT', 'UPDATE', 'DELETE']) p
		              WHERE NOT has_table_privilege(r.oid, t.oid, p)),
		       ARRAY(SELECT x.privilege_type || ' (' || string_agg(a.attname, ', ' ORDER BY a.attnum) || ')'
		               FROM pg_attribute a, aclexplode(a.attacl) x
		              WHERE a.attrelid = t.oid AND a.attacl IS NOT NULL AND x.grantee = r.oid
		              GROUP BY x.privilege_type ORDER BY 1)
		  FROM rels t CROSS JOIN roles r
		 ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("read table privileges: %w", err)
	}
	for rows.Next() {
		var obj, role string
		var missing, cols []string
		if err := rows.Scan(&obj, &role, &missing, &cols); err != nil {
			rows.Close()
			return nil, err
		}
		if len(missing) == 0 {
			continue
		}
		detail := "no " + strings.Join(missing, ", ")
		if len(cols) > 0 {
			detail += "; column grants: " + strings.Join(cols, "; ")
		}
		out = append(out, narrowedGrant{object: obj, role: role, detail: detail})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read table privileges: %w", err)
	}

	rows, err = conn.Query(ctx, `
		SELECT 'function ' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', r.rolname
		  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		  CROSS JOIN (SELECT oid, rolname FROM pg_roles WHERE rolname IN ('anon', 'authenticated')) r
		 WHERE n.nspname = 'public' AND p.prokind IN ('f', 'p')
		   -- Trigger functions can't be called directly, with or without
		   -- EXECUTE (revoking it is what Supabase's advisor recommends).
		   AND p.prorettype NOT IN ('trigger'::regtype, 'event_trigger'::regtype)
		   AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                    WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
		   AND NOT has_function_privilege(r.oid, p.oid, 'EXECUTE')
		 ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("read function privileges: %w", err)
	}
	for rows.Next() {
		var obj, role string
		if err := rows.Scan(&obj, &role); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, narrowedGrant{object: obj, role: role, detail: "no EXECUTE"})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read function privileges: %w", err)
	}
	return out, nil
}

// grantsNote explains what a narrowed grant means after the import.
const grantsNote = "Eurobase has no per-role grants: every API request can use every table, column and function of the project, and RLS alone decides which rows. Restrictions made with GRANT / REVOKE are not carried over. Protect these with RLS policies, a security_invoker view that leaves out the columns, or an edge function, before your app goes live on Eurobase."
