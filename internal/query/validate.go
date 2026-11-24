package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ForeignKeyInfo describes a foreign key constraint on a column.
type ForeignKeyInfo struct {
	ConstraintName   string `json:"constraint_name"`
	ReferencedTable  string `json:"referenced_table"`
	ReferencedColumn string `json:"referenced_column"`
}

// IndexInfo describes an index on a table.
type IndexInfo struct {
	Name     string `json:"name"`
	Column   string `json:"column"`
	IsUnique bool   `json:"is_unique"`
}

// ColumnInfo holds metadata about a single table column.
type ColumnInfo struct {
	Name         string          `json:"name"`
	DataType     string          `json:"data_type"`
	IsNullable   bool            `json:"is_nullable"`
	DefaultValue *string         `json:"default_value,omitempty"`
	IsPrimaryKey bool            `json:"is_primary_key"`
	IsUnique     bool            `json:"is_unique"`
	ForeignKey   *ForeignKeyInfo `json:"foreign_key,omitempty"`
}

// ValidateTable checks that a table exists in the given schema by querying pg_catalog.
func ValidateTable(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string) error {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_tables
			WHERE schemaname = $1 AND tablename = $2
		)`,
		schemaName, tableName,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check table existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("table %q does not exist in schema %q", tableName, schemaName)
	}
	return nil
}

// ValidateColumns checks that all specified columns exist in the given table.
func ValidateColumns(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string, columns []string) error {
	if len(columns) == 0 {
		return nil
	}

	// Get all valid column names for this table.
	rows, err := pool.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2`,
		schemaName, tableName,
	)
	if err != nil {
		return fmt.Errorf("query columns: %w", err)
	}
	defer rows.Close()

	validCols := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan column name: %w", err)
		}
		validCols[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate columns: %w", err)
	}

	var invalid []string
	for _, c := range columns {
		if !validCols[c] {
			invalid = append(invalid, c)
		}
	}

	if len(invalid) > 0 {
		return fmt.Errorf("invalid columns: %s", strings.Join(invalid, ", "))
	}
	return nil
}

// GetTableColumns returns all columns with metadata for the given table.
func GetTableColumns(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string) ([]ColumnInfo, error) {
	byTable, err := schemaColumns(ctx, pool, schemaName, &tableName)
	if err != nil {
		return nil, err
	}
	return byTable[tableName], nil
}

// schemaColumns returns the columns of every table in the schema (or of one
// table when table is set), keyed by table, in column order. One query for
// the whole schema, so listing many tables costs one round trip, not one
// per table.
func schemaColumns(ctx context.Context, pool *pgxpool.Pool, schemaName string, table *string) (map[string][]ColumnInfo, error) {
	rows, err := pool.Query(ctx,
		`SELECT table_name, column_name, data_type, is_nullable, column_default
		 FROM information_schema.columns
		 WHERE table_schema = $1 AND ($2::text IS NULL OR table_name = $2)
		 ORDER BY table_name, ordinal_position`,
		schemaName, table,
	)
	if err != nil {
		return nil, fmt.Errorf("query table columns: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]ColumnInfo)
	for rows.Next() {
		var tbl string
		var ci ColumnInfo
		var nullable string
		var defaultVal *string
		if err := rows.Scan(&tbl, &ci.Name, &ci.DataType, &nullable, &defaultVal); err != nil {
			return nil, fmt.Errorf("scan column info: %w", err)
		}
		ci.IsNullable = (nullable == "YES")
		ci.DefaultValue = defaultVal
		out[tbl] = append(out[tbl], ci)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate column rows: %w", err)
	}
	return out, nil
}

// GetTableConstraints enriches a slice of ColumnInfo with PK, FK, and unique constraint metadata.
//
// It reads pg_constraint, not information_schema: constraint_column_usage
// only lists a referenced table to a role that owns it, and the runtime
// pool owns no tenant tables, so every foreign key came back without its
// target. Composite keys map column by column (conkey[i] → confkey[i]).
// An FK to a partitioned table also has one clone row per partition on the
// same table (conparentid set); those are skipped so the FK names the
// partitioned table. A partition's own inherited FK (parent on another
// table) is kept. UNIQUE marks a column only when it is unique on its own.
func GetTableConstraints(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string, columns []ColumnInfo) ([]ColumnInfo, error) {
	byTable, err := schemaConstraints(ctx, pool, schemaName, &tableName)
	if err != nil {
		return columns, err
	}
	return applyConstraints(columns, byTable[tableName]), nil
}

// constraintRow is one column of a PK / FK / single-column UNIQUE constraint.
type constraintRow struct {
	kind, name, column string
	refTable, refCol   *string
}

// schemaConstraints reads the constraints of every table in the schema (or
// of one table), keyed by table. Same rules as GetTableConstraints.
func schemaConstraints(ctx context.Context, pool *pgxpool.Pool, schemaName string, table *string) (map[string][]constraintRow, error) {
	rows, err := pool.Query(ctx,
		`SELECT t.relname,
		        c.contype::text,
		        c.conname,
		        a.attname,
		        rt.relname AS referenced_table,
		        ra.attname AS referenced_column
		   FROM pg_catalog.pg_constraint c
		   JOIN pg_catalog.pg_class t     ON t.oid = c.conrelid
		   JOIN pg_catalog.pg_namespace n ON n.oid = t.relnamespace
		   CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		   JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		   LEFT JOIN pg_catalog.pg_class rt     ON rt.oid = c.confrelid
		   LEFT JOIN pg_catalog.pg_attribute ra ON ra.attrelid = c.confrelid AND ra.attnum = c.confkey[k.ord]
		  WHERE n.nspname = $1 AND ($2::text IS NULL OR t.relname = $2)
		    AND c.contype IN ('p', 'f', 'u')
		    AND (c.contype <> 'u' OR cardinality(c.conkey) = 1)
		    AND (c.conparentid = 0 OR NOT EXISTS (
		          SELECT 1 FROM pg_catalog.pg_constraint pc
		           WHERE pc.oid = c.conparentid AND pc.conrelid = c.conrelid))
		  ORDER BY t.relname, c.conname, k.ord`,
		schemaName, table,
	)
	if err != nil {
		return nil, fmt.Errorf("query table constraints: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]constraintRow)
	for rows.Next() {
		var tbl string
		var cr constraintRow
		if err := rows.Scan(&tbl, &cr.kind, &cr.name, &cr.column, &cr.refTable, &cr.refCol); err != nil {
			return nil, fmt.Errorf("scan constraint: %w", err)
		}
		out[tbl] = append(out[tbl], cr)
	}
	return out, rows.Err()
}

// applyConstraints marks PK / UNIQUE / FK on the matching columns.
func applyConstraints(columns []ColumnInfo, cons []constraintRow) []ColumnInfo {
	colMap := make(map[string]int, len(columns))
	for i, c := range columns {
		colMap[c.Name] = i
	}
	for _, cr := range cons {
		idx, ok := colMap[cr.column]
		if !ok {
			continue
		}
		switch cr.kind {
		case "p":
			columns[idx].IsPrimaryKey = true
		case "u":
			columns[idx].IsUnique = true
		case "f":
			if cr.refTable != nil && cr.refCol != nil {
				columns[idx].ForeignKey = &ForeignKeyInfo{
					ConstraintName:   cr.name,
					ReferencedTable:  *cr.refTable,
					ReferencedColumn: *cr.refCol,
				}
			}
		}
	}
	return columns
}

// GetTableIndexes returns user-created indexes for a table.
// Indexes that back constraints (PK, unique, FK) are excluded — those are
// managed via the constraint APIs and cannot be dropped with DROP INDEX.
func GetTableIndexes(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string) ([]IndexInfo, error) {
	byTable, err := schemaIndexes(ctx, pool, schemaName, &tableName)
	if err != nil {
		return nil, err
	}
	return byTable[tableName], nil
}

// schemaIndexes returns the user-created indexes of every table in the
// schema (or of one table), keyed by table. Same rules as GetTableIndexes.
func schemaIndexes(ctx context.Context, pool *pgxpool.Pool, schemaName string, table *string) (map[string][]IndexInfo, error) {
	rows, err := pool.Query(ctx,
		`SELECT i.tablename, i.indexname, i.indexdef
		 FROM pg_indexes i
		 LEFT JOIN pg_constraint c
		   ON c.conindid = (
		     SELECT oid FROM pg_class
		     WHERE relname = i.indexname
		       AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = i.schemaname)
		   )
		 WHERE i.schemaname = $1 AND ($2::text IS NULL OR i.tablename = $2)
		   AND c.oid IS NULL
		 ORDER BY i.tablename, i.indexname`,
		schemaName, table,
	)
	if err != nil {
		return nil, fmt.Errorf("query table indexes: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]IndexInfo)
	for rows.Next() {
		var tbl, name, def string
		if err := rows.Scan(&tbl, &name, &def); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}
		isUnique := strings.Contains(strings.ToUpper(def), "UNIQUE")
		out[tbl] = append(out[tbl], IndexInfo{
			Name:     name,
			Column:   indexColumns(def),
			IsUnique: isUnique,
		})
	}
	return out, rows.Err()
}

// indexColumns returns what an index is on, from its definition
// ("CREATE INDEX i ON s.t USING btree (a, lower(b)) WHERE …" → "a, lower(b)"):
// the text inside the parentheses after USING <method>, matched as a pair,
// with a lone quoted column unquoted.
func indexColumns(def string) string {
	start := strings.Index(def, " USING ")
	if start < 0 {
		start = 0
	}
	open := strings.Index(def[start:], "(")
	if open < 0 {
		return ""
	}
	open += start
	depth := 0
	for i := open; i < len(def); i++ {
		switch def[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.Trim(def[open+1:i], " \"")
			}
		}
	}
	return ""
}

// rlsFlags is a table's row-level security state: On (ENABLE ROW LEVEL
// SECURITY) and Force (FORCE ROW LEVEL SECURITY: the table's owner is
// subject to the policies too).
type rlsFlags struct {
	On    bool
	Force bool
}

// schemaRLS returns, per table in the schema, its row-level security flags.
func schemaRLS(ctx context.Context, pool *pgxpool.Pool, schemaName string) (map[string]rlsFlags, error) {
	rows, err := pool.Query(ctx,
		`SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity
		   FROM pg_catalog.pg_class c
		   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')`,
		schemaName,
	)
	if err != nil {
		return nil, fmt.Errorf("query rls flags: %w", err)
	}
	defer rows.Close()
	out := make(map[string]rlsFlags)
	for rows.Next() {
		var tbl string
		var f rlsFlags
		if err := rows.Scan(&tbl, &f.On, &f.Force); err != nil {
			return nil, fmt.Errorf("scan rls flag: %w", err)
		}
		out[tbl] = f
	}
	return out, rows.Err()
}

// GetSchemaTables returns all table names in the given schema.
func GetSchemaTables(ctx context.Context, pool *pgxpool.Pool, schemaName string) ([]string, error) {
	rows, err := pool.Query(ctx,
		`SELECT tablename FROM pg_catalog.pg_tables
		 WHERE schemaname = $1
		 ORDER BY tablename`,
		schemaName,
	)
	if err != nil {
		return nil, fmt.Errorf("query schema tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table rows: %w", err)
	}

	return tables, nil
}
