package query

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// validIdentRe matches safe SQL identifiers (letters, digits, underscores).
var validIdentRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// reservedTableNames are system tables that cannot be dropped or altered.
var reservedTableNames = map[string]bool{
	"storage_objects": true,
}

// ColumnDefinition describes a column to create.
type ColumnDefinition struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	DefaultValue string `json:"default_value,omitempty"`
	IsPrimaryKey bool   `json:"is_primary_key"`
}

// allowedTypes are PostgreSQL types that can be used in table creation.
var allowedTypes = map[string]bool{
	"text": true, "integer": true, "bigint": true, "smallint": true,
	"boolean": true, "uuid": true, "timestamp": true, "timestamptz": true,
	"jsonb": true, "json": true, "real": true, "numeric": true,
	"date": true, "time": true, "bytea": true, "serial": true, "bigserial": true,
	"double precision": true, "character varying": true, "varchar": true,
}

// validateIdentifier checks that a name is a safe SQL identifier.
func validateIdentifier(name, kind string) error {
	if name == "" {
		return fmt.Errorf("%s name cannot be empty", kind)
	}
	if !validIdentRe.MatchString(name) {
		return fmt.Errorf("%s name %q contains invalid characters (use letters, digits, underscores)", kind, name)
	}
	if len(name) > 63 {
		return fmt.Errorf("%s name %q is too long (max 63 characters)", kind, name)
	}
	return nil
}

// validateColumnType checks that a type is in the allowed list.
func validateColumnType(colType string) error {
	lower := strings.ToLower(strings.TrimSpace(colType))
	if !allowedTypes[lower] {
		return fmt.Errorf("unsupported column type %q", colType)
	}
	return nil
}

// validateDefault checks that a default value expression is safe.
// Only allows simple literals and known function calls.
func validateDefault(def string) error {
	if def == "" {
		return nil
	}
	upper := strings.ToUpper(strings.TrimSpace(def))
	// Allow known safe defaults
	safe := []string{
		"NOW()", "CURRENT_TIMESTAMP", "TRUE", "FALSE",
		"UUID_GENERATE_V4()", "PUBLIC.UUID_GENERATE_V4()",
		"GEN_RANDOM_UUID()", "0", "1", "''",
		"'{}'::JSONB", "'{}'::JSON", "'[]'::JSONB", "'[]'::JSON",
	}
	for _, s := range safe {
		if upper == s {
			return nil
		}
	}
	// Allow simple string/numeric literals
	trimmed := strings.TrimSpace(def)
	if isSimpleLiteral(trimmed) {
		return nil
	}
	return fmt.Errorf("unsupported default value %q — use a literal or a known function like now(), uuid_generate_v4()", def)
}

// isSimpleLiteral checks if a string is a simple SQL literal (number or quoted string).
func isSimpleLiteral(s string) bool {
	// Numeric
	if matched, _ := regexp.MatchString(`^-?\d+(\.\d+)?$`, s); matched {
		return true
	}
	// Single-quoted string (no embedded quotes to prevent injection)
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' && !strings.Contains(s[1:len(s)-1], "'") {
		return true
	}
	return false
}

// CreateTable creates a new table in the tenant's schema with RLS enabled.
func CreateTable(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string, columns []ColumnDefinition) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}

	if len(columns) == 0 {
		return fmt.Errorf("at least one column is required")
	}

	// Validate all columns first.
	for _, col := range columns {
		if err := validateIdentifier(col.Name, "column"); err != nil {
			return err
		}
		if err := validateColumnType(col.Type); err != nil {
			return err
		}
		if err := validateDefault(col.DefaultValue); err != nil {
			return err
		}
	}

	// Check table doesn't already exist.
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
	if exists {
		return fmt.Errorf("table %s already exists", tableName)
	}

	// Build CREATE TABLE SQL.
	qt := qualifiedTable(schemaName, tableName)
	var colDefs []string
	var pks []string

	for _, col := range columns {
		parts := []string{quoteIdent(col.Name), strings.ToUpper(col.Type)}
		if !col.Nullable {
			parts = append(parts, "NOT NULL")
		}
		if col.DefaultValue != "" {
			parts = append(parts, "DEFAULT "+col.DefaultValue)
		}
		colDefs = append(colDefs, strings.Join(parts, " "))
		if col.IsPrimaryKey {
			pks = append(pks, quoteIdent(col.Name))
		}
	}

	if len(pks) > 0 {
		colDefs = append(colDefs, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(pks, ", ")))
	}

	createSQL := fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", qt, strings.Join(colDefs, ",\n  "))

	// Execute in a transaction: CREATE TABLE + enable RLS + create policy.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Enable RLS.
	rlsSQL := fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", qt)
	if _, err := tx.Exec(ctx, rlsSQL); err != nil {
		return fmt.Errorf("enable RLS: %w", err)
	}

	// Create a permissive policy (allows all operations within the tenant schema).
	// Tenant isolation is enforced by schema separation.
	policySQL := fmt.Sprintf(
		"CREATE POLICY tenant_isolation_%s ON %s FOR ALL USING (true)",
		tableName, qt,
	)
	if _, err := tx.Exec(ctx, policySQL); err != nil {
		return fmt.Errorf("create RLS policy: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// DropTable drops a table from the tenant's schema.
func DropTable(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}

	if reservedTableNames[tableName] {
		return fmt.Errorf("cannot drop system table %q", tableName)
	}

	// Verify table exists.
	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	sql := fmt.Sprintf("DROP TABLE %s", qt)
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("drop table: %w", err)
	}

	return nil
}

// AddColumn adds a column to an existing table.
func AddColumn(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string, col ColumnDefinition) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(col.Name, "column"); err != nil {
		return err
	}
	if err := validateColumnType(col.Type); err != nil {
		return err
	}
	if err := validateDefault(col.DefaultValue); err != nil {
		return err
	}

	// Verify table exists.
	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	parts := []string{fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", qt, quoteIdent(col.Name), strings.ToUpper(col.Type))}
	if !col.Nullable {
		parts = append(parts, "NOT NULL")
	}
	if col.DefaultValue != "" {
		parts = append(parts, "DEFAULT "+col.DefaultValue)
	}

	sql := strings.Join(parts, " ")
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("add column: %w", err)
	}

	return nil
}

// DropColumn removes a column from a table.
func DropColumn(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, columnName string) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(columnName, "column"); err != nil {
		return err
	}

	// Verify table and column exist.
	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}
	if err := ValidateColumns(ctx, pool, schemaName, tableName, []string{columnName}); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	sql := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", qt, quoteIdent(columnName))
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("drop column: %w", err)
	}

	return nil
}
