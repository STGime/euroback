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
	rows, err := pool.Query(ctx,
		`SELECT column_name, data_type, is_nullable, column_default
		 FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2
		 ORDER BY ordinal_position`,
		schemaName, tableName,
	)
	if err != nil {
		return nil, fmt.Errorf("query table columns: %w", err)
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var ci ColumnInfo
		var nullable string
		var defaultVal *string
		if err := rows.Scan(&ci.Name, &ci.DataType, &nullable, &defaultVal); err != nil {
			return nil, fmt.Errorf("scan column info: %w", err)
		}
		ci.IsNullable = (nullable == "YES")
		ci.DefaultValue = defaultVal
		columns = append(columns, ci)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate column rows: %w", err)
	}

	return columns, nil
}

// GetTableConstraints enriches a slice of ColumnInfo with PK, FK, and unique constraint metadata.
//
// It reads pg_constraint, not information_schema: constraint_column_usage
// only lists a referenced table to a role that owns it, and the runtime
// pool owns no tenant tables, so every foreign key came back without its
// target. Composite keys map column by column (conkey[i] → confkey[i]).
func GetTableConstraints(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string, columns []ColumnInfo) ([]ColumnInfo, error) {
	rows, err := pool.Query(ctx,
		`SELECT c.contype::text,
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
		  WHERE n.nspname = $1 AND t.relname = $2
		    AND c.contype IN ('p', 'f', 'u')
		  ORDER BY c.conname, k.ord`,
		schemaName, tableName,
	)
	if err != nil {
		return columns, fmt.Errorf("query table constraints: %w", err)
	}
	defer rows.Close()

	colMap := make(map[string]int, len(columns))
	for i, c := range columns {
		colMap[c.Name] = i
	}

	for rows.Next() {
		var constraintType, constraintName, colName string
		var refTable, refCol *string
		if err := rows.Scan(&constraintType, &constraintName, &colName, &refTable, &refCol); err != nil {
			return columns, fmt.Errorf("scan constraint: %w", err)
		}
		idx, ok := colMap[colName]
		if !ok {
			continue
		}
		switch constraintType {
		case "p":
			columns[idx].IsPrimaryKey = true
		case "u":
			columns[idx].IsUnique = true
		case "f":
			if refTable != nil && refCol != nil {
				columns[idx].ForeignKey = &ForeignKeyInfo{
					ConstraintName:   constraintName,
					ReferencedTable:  *refTable,
					ReferencedColumn: *refCol,
				}
			}
		}
	}

	return columns, rows.Err()
}

// GetTableIndexes returns user-created indexes for a table.
// Indexes that back constraints (PK, unique, FK) are excluded — those are
// managed via the constraint APIs and cannot be dropped with DROP INDEX.
func GetTableIndexes(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string) ([]IndexInfo, error) {
	rows, err := pool.Query(ctx,
		`SELECT i.indexname, i.indexdef
		 FROM pg_indexes i
		 LEFT JOIN pg_constraint c
		   ON c.conindid = (
		     SELECT oid FROM pg_class
		     WHERE relname = i.indexname
		       AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = i.schemaname)
		   )
		 WHERE i.schemaname = $1 AND i.tablename = $2
		   AND c.oid IS NULL`,
		schemaName, tableName,
	)
	if err != nil {
		return nil, fmt.Errorf("query table indexes: %w", err)
	}
	defer rows.Close()

	var indexes []IndexInfo
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}
		isUnique := strings.Contains(strings.ToUpper(def), "UNIQUE")
		// Parse column from indexdef: ... ON schema.table USING btree (column)
		col := ""
		if lparen := strings.LastIndex(def, "("); lparen >= 0 {
			if rparen := strings.LastIndex(def, ")"); rparen > lparen {
				col = strings.Trim(def[lparen+1:rparen], " \"")
			}
		}
		indexes = append(indexes, IndexInfo{
			Name:     name,
			Column:   col,
			IsUnique: isUnique,
		})
	}

	return indexes, rows.Err()
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
