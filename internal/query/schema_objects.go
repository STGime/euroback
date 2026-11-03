package query

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// #874: GET /platform/projects/{id}/schema/objects lists the schema
// objects GET /schema (tables) and /schema/functions don't: views,
// materialized views, sequences and types. Developer SQL can't read the
// catalog, so this is the self-serve way to see them (e.g. to check for
// name conflicts before a migration).
//
// Names are viewer-level, like the table listing; view definitions are
// developer+, like function definitions. Only the project's own schema,
// never extension members.

// SchemaView is a view or materialized view.
type SchemaView struct {
	Name         string `json:"name"`
	Materialized bool   `json:"materialized"`
	// SecurityInvoker: the view checks the reader's RLS (false for a
	// materialized view, which stores rows).
	SecurityInvoker bool   `json:"security_invoker"`
	Definition      string `json:"definition,omitempty"`
}

// SchemaSequence is a sequence, with the column it belongs to (serial or
// identity columns, OWNED BY), if any.
type SchemaSequence struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	OwnedBy  string `json:"owned_by,omitempty"` // "table.column"
}

// SchemaType is a standalone type: enum, composite, domain, range, or a
// shell (`CREATE TYPE name;` not yet defined).
type SchemaType struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Labels   []string `json:"labels,omitempty"`    // enum values, in order
	BaseType string   `json:"base_type,omitempty"` // domain base / range subtype
	// Multirange: the type PostgreSQL creates with each range; its name is
	// taken too.
	Multirange string `json:"multirange,omitempty"`
}

// SchemaObjects is the response of GET …/schema/objects.
type SchemaObjects struct {
	Views     []SchemaView     `json:"views"`
	Sequences []SchemaSequence `json:"sequences"`
	Types     []SchemaType     `json:"types"`
}

// notExtensionMember excludes objects an extension created; $class is
// the catalog the object lives in.
func notExtensionMember(class, oid string) string {
	return `NOT EXISTS (SELECT 1 FROM pg_depend d
	                     WHERE d.classid = '` + class + `'::regclass AND d.objid = ` + oid + `
	                       AND d.refclassid = 'pg_extension'::regclass AND d.deptype = 'e')`
}

// ListSchemaObjects reads the views, sequences and types of schemaName.
// withDefinitions adds view definitions. Sequences owned by a platform
// table are hidden when hideInternal is set (as the table listing hides
// the tables themselves for a viewer).
func ListSchemaObjects(ctx context.Context, pool *pgxpool.Pool, schemaName string, withDefinitions, hideInternal bool) (*SchemaObjects, error) {
	pool, perr := tenantDDLPool(ctx, pool)
	if perr != nil {
		return nil, perr
	}
	out := &SchemaObjects{Views: []SchemaView{}, Sequences: []SchemaSequence{}, Types: []SchemaType{}}

	// One read-only snapshot for the three listings, with a time limit.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("list schema objects: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '10s'`); err != nil {
		return nil, fmt.Errorf("list schema objects: %w", err)
	}

	// Views and materialized views.
	def := `''`
	if withDefinitions {
		// NULL if the view was dropped since the scan (it reads the
		// catalog cache, not the snapshot).
		def = `COALESCE(pg_get_viewdef(c.oid, true), '')`
	}
	rows, err := tx.Query(ctx, `
		SELECT c.relname, c.relkind = 'm', COALESCE(c.reloptions, '{}'), `+def+`
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relkind IN ('v', 'm')
		   AND `+notExtensionMember("pg_class", "c.oid")+`
		 ORDER BY c.relname`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	for rows.Next() {
		var v SchemaView
		var opts []string
		if err := rows.Scan(&v.Name, &v.Materialized, &opts, &v.Definition); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan view: %w", err)
		}
		v.SecurityInvoker = !v.Materialized && securityInvokerOn(opts)
		out.Views = append(out.Views, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}

	// Sequences, with the owning column (OWNED BY / serial: 'a',
	// identity: 'i').
	rows, err = tx.Query(ctx, `
		SELECT c.relname, format_type(s.seqtypid, NULL),
		       COALESCE(t.relname, ''), COALESCE(a.attname, '')
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_sequence s ON s.seqrelid = c.oid
		  LEFT JOIN pg_depend d ON d.classid = 'pg_class'::regclass AND d.objid = c.oid
		                       AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
		  LEFT JOIN pg_class t ON t.oid = d.refobjid
		  LEFT JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
		 WHERE n.nspname = $1 AND c.relkind = 'S'
		   AND `+notExtensionMember("pg_class", "c.oid")+`
		 ORDER BY c.relname`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list sequences: %w", err)
	}
	for rows.Next() {
		var s SchemaSequence
		var table, column string
		if err := rows.Scan(&s.Name, &s.DataType, &table, &column); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan sequence: %w", err)
		}
		if table != "" && hideInternal && internalTenantTables[table] {
			continue
		}
		if table != "" && column != "" {
			s.OwnedBy = table + "." + column
		}
		out.Sequences = append(out.Sequences, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sequences: %w", err)
	}

	// Standalone types. A table's or view's row type isn't one (composite
	// types count only for CREATE TYPE … AS, relkind 'c'); array types are
	// left out, and a range's multirange is reported with the range.
	rows, err = tx.Query(ctx, `
		SELECT t.typname,
		       CASE t.typtype WHEN 'e' THEN 'enum' WHEN 'c' THEN 'composite'
		                      WHEN 'd' THEN 'domain' WHEN 'r' THEN 'range' ELSE 'shell' END,
		       COALESCE((SELECT array_agg(e.enumlabel::text ORDER BY e.enumsortorder)
		                   FROM pg_enum e WHERE e.enumtypid = t.oid), '{}'),
		       CASE t.typtype
		            WHEN 'd' THEN format_type(t.typbasetype, t.typtypmod)
		            WHEN 'r' THEN (SELECT format_type(r.rngsubtype, NULL) FROM pg_range r WHERE r.rngtypid = t.oid)
		            ELSE '' END,
		       COALESCE((SELECT mt.typname FROM pg_range r JOIN pg_type mt ON mt.oid = r.rngmultitypid
		                  WHERE r.rngtypid = t.oid), '')
		  FROM pg_type t
		  JOIN pg_namespace n ON n.oid = t.typnamespace
		 WHERE n.nspname = $1
		   AND (t.typtype IN ('e', 'c', 'd', 'r') OR (t.typtype = 'p' AND NOT t.typisdefined))
		   AND (t.typtype <> 'c' OR EXISTS (SELECT 1 FROM pg_class c WHERE c.oid = t.typrelid AND c.relkind = 'c'))
		   AND `+notExtensionMember("pg_type", "t.oid")+`
		 ORDER BY t.typname`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list types: %w", err)
	}
	for rows.Next() {
		var ty SchemaType
		if err := rows.Scan(&ty.Name, &ty.Kind, &ty.Labels, &ty.BaseType, &ty.Multirange); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan type: %w", err)
		}
		// format_type qualifies a type from this schema when it isn't on
		// the search_path (shared vs. Team pools differ): show it unqualified.
		ty.BaseType = strings.TrimPrefix(ty.BaseType, schemaName+".")
		ty.BaseType = strings.TrimPrefix(ty.BaseType, `"`+schemaName+`".`)
		out.Types = append(out.Types, ty)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list types: %w", err)
	}
	return out, nil
}

// HandleSchemaObjects answers GET …/schema/objects (viewer+). View
// definitions are included for developer and up.
func HandleSchemaObjects(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			jsonError(w, "project ID is required", http.StatusBadRequest)
			return
		}
		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}
		// Definitions and platform-table sequences need developer and up;
		// any other role (viewer, unknown, unset) gets names only, so the
		// route fails closed if it's ever mounted without the role middleware.
		full := RoleSeesInternalTables(PlatformRoleFromContext(r.Context()))
		objs, err := ListSchemaObjects(r.Context(), pool, schemaName, full, !full)
		if err != nil {
			if errors.Is(err, ErrDedicatedPoolUnavailable) {
				jsonError(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			slog.Error("list schema objects failed", "error", err, "schema", schemaName)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, objs, http.StatusOK)
	}
}
