package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ColumnInfo holds metadata about a single table column.
type ColumnInfo struct {
	Name         string  `json:"name"`
	DataType     string  `json:"data_type"`
	IsNullable   bool    `json:"is_nullable"`
	DefaultValue *string `json:"default_value,omitempty"`
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
