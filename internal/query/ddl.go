package query

import (
	"context"
	"fmt"
	"log/slog"
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

// ForeignKeyDefinition describes a foreign key to add.
type ForeignKeyDefinition struct {
	Column           string `json:"column"`
	ReferencedTable  string `json:"referenced_table"`
	ReferencedColumn string `json:"referenced_column"`
	OnDelete         string `json:"on_delete,omitempty"` // CASCADE, SET NULL, RESTRICT, NO ACTION (default)
}

// ColumnDefinition describes a column to create.
type ColumnDefinition struct {
	Name         string                `json:"name"`
	Type         string                `json:"type"`
	Nullable     bool                  `json:"nullable"`
	DefaultValue string                `json:"default_value,omitempty"`
	IsPrimaryKey bool                  `json:"is_primary_key"`
	IsUnique     bool                  `json:"is_unique,omitempty"`
	ForeignKey   *ForeignKeyDefinition `json:"foreign_key,omitempty"`
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

	// Add UNIQUE constraints inline.
	for _, col := range columns {
		if col.IsUnique {
			colDefs = append(colDefs, fmt.Sprintf("CONSTRAINT %s UNIQUE (%s)",
				quoteIdent(fmt.Sprintf("uq_%s_%s", tableName, col.Name)),
				quoteIdent(col.Name)))
		}
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

	// Enable RLS (no default policy — table is locked down until policies are added).
	rlsSQL := fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", qt)
	if _, err := tx.Exec(ctx, rlsSQL); err != nil {
		return fmt.Errorf("enable RLS: %w", err)
	}

	// Add foreign key constraints.
	for _, col := range columns {
		if col.ForeignKey != nil {
			fk := col.ForeignKey
			onDelete := "NO ACTION"
			if fk.OnDelete != "" {
				onDelete = strings.ToUpper(fk.OnDelete)
			}
			fkSQL := fmt.Sprintf(
				"ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s.%s(%s) ON DELETE %s",
				qt,
				quoteIdent(fmt.Sprintf("fk_%s_%s", tableName, col.Name)),
				quoteIdent(col.Name),
				quoteIdent(schemaName), quoteIdent(fk.ReferencedTable),
				quoteIdent(fk.ReferencedColumn),
				onDelete,
			)
			if _, err := tx.Exec(ctx, fkSQL); err != nil {
				return fmt.Errorf("add foreign key on %s: %w", col.Name, err)
			}
		}
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

// RenameTable renames a table and its RLS policy.
func RenameTable(ctx context.Context, pool *pgxpool.Pool, schemaName, oldName, newName string) error {
	if err := validateIdentifier(oldName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(newName, "table"); err != nil {
		return err
	}
	if reservedTableNames[oldName] {
		return fmt.Errorf("cannot rename system table %q", oldName)
	}
	if reservedTableNames[newName] {
		return fmt.Errorf("cannot rename to reserved table name %q", newName)
	}
	if oldName == newName {
		return fmt.Errorf("new name is the same as the current name")
	}

	if err := ValidateTable(ctx, pool, schemaName, oldName); err != nil {
		return err
	}

	// Check new name doesn't already exist.
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = $1 AND tablename = $2)`,
		schemaName, newName,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check table existence: %w", err)
	}
	if exists {
		return fmt.Errorf("table %s already exists", newName)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qt := qualifiedTable(schemaName, oldName)
	renameSQL := fmt.Sprintf("ALTER TABLE %s RENAME TO %s", qt, quoteIdent(newName))
	if _, err := tx.Exec(ctx, renameSQL); err != nil {
		return fmt.Errorf("rename table: %w", err)
	}

	// Rename the RLS policy (best-effort via savepoint so failure doesn't abort the tx).
	oldPolicy := fmt.Sprintf("tenant_isolation_%s", oldName)
	newPolicy := fmt.Sprintf("tenant_isolation_%s", newName)
	newQt := qualifiedTable(schemaName, newName)
	if _, err := tx.Exec(ctx, "SAVEPOINT rename_policy"); err == nil {
		policySQL := fmt.Sprintf("ALTER POLICY %s ON %s RENAME TO %s", quoteIdent(oldPolicy), newQt, quoteIdent(newPolicy))
		if _, err := tx.Exec(ctx, policySQL); err != nil {
			slog.Warn("failed to rename RLS policy (non-fatal)", "error", err)
			tx.Exec(ctx, "ROLLBACK TO SAVEPOINT rename_policy") //nolint:errcheck
		} else {
			tx.Exec(ctx, "RELEASE SAVEPOINT rename_policy") //nolint:errcheck
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// RenameColumn renames a column in a table.
func RenameColumn(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, oldCol, newCol string) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(oldCol, "column"); err != nil {
		return err
	}
	if err := validateIdentifier(newCol, "column"); err != nil {
		return err
	}
	if oldCol == newCol {
		return fmt.Errorf("new column name is the same as the current name")
	}

	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}
	if err := ValidateColumns(ctx, pool, schemaName, tableName, []string{oldCol}); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	sql := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", qt, quoteIdent(oldCol), quoteIdent(newCol))
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("rename column: %w", err)
	}

	return nil
}

// AlterColumnType changes the data type of a column.
func AlterColumnType(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, col, newType string) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(col, "column"); err != nil {
		return err
	}
	if err := validateColumnType(newType); err != nil {
		return err
	}

	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}
	if err := ValidateColumns(ctx, pool, schemaName, tableName, []string{col}); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	sql := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s",
		qt, quoteIdent(col), strings.ToUpper(newType), quoteIdent(col), strings.ToUpper(newType))
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("alter column type: %w", err)
	}

	return nil
}

// AlterColumnNullable sets or drops the NOT NULL constraint on a column.
func AlterColumnNullable(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, col string, nullable bool) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(col, "column"); err != nil {
		return err
	}

	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}
	if err := ValidateColumns(ctx, pool, schemaName, tableName, []string{col}); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	action := "SET NOT NULL"
	if nullable {
		action = "DROP NOT NULL"
	}
	sql := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s", qt, quoteIdent(col), action)
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("alter column nullable: %w", err)
	}

	return nil
}

// validOnDelete lists the allowed ON DELETE actions for FK constraints.
var validOnDelete = map[string]bool{
	"CASCADE": true, "SET NULL": true, "RESTRICT": true, "NO ACTION": true,
}

// AddForeignKey adds a foreign key constraint to a column.
func AddForeignKey(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string, fk ForeignKeyDefinition) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(fk.Column, "column"); err != nil {
		return err
	}
	if err := validateIdentifier(fk.ReferencedTable, "referenced table"); err != nil {
		return err
	}
	if err := validateIdentifier(fk.ReferencedColumn, "referenced column"); err != nil {
		return err
	}

	onDelete := "NO ACTION"
	if fk.OnDelete != "" {
		onDelete = strings.ToUpper(fk.OnDelete)
		if !validOnDelete[onDelete] {
			return fmt.Errorf("invalid ON DELETE action %q", fk.OnDelete)
		}
	}

	// Verify source table/column exist.
	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}
	if err := ValidateColumns(ctx, pool, schemaName, tableName, []string{fk.Column}); err != nil {
		return err
	}
	// Verify referenced table/column exist.
	if err := ValidateTable(ctx, pool, schemaName, fk.ReferencedTable); err != nil {
		return fmt.Errorf("referenced table: %w", err)
	}
	if err := ValidateColumns(ctx, pool, schemaName, fk.ReferencedTable, []string{fk.ReferencedColumn}); err != nil {
		return fmt.Errorf("referenced column: %w", err)
	}

	qt := qualifiedTable(schemaName, tableName)
	constraintName := fmt.Sprintf("fk_%s_%s", tableName, fk.Column)

	sql := fmt.Sprintf(
		"ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s.%s(%s) ON DELETE %s",
		qt, quoteIdent(constraintName), quoteIdent(fk.Column),
		quoteIdent(schemaName), quoteIdent(fk.ReferencedTable), quoteIdent(fk.ReferencedColumn),
		onDelete,
	)
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("add foreign key: %w", err)
	}

	return nil
}

// DropConstraint drops a named constraint from a table (works for FK and UNIQUE).
func DropConstraint(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, constraintName string) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	sql := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", qt, quoteIdent(constraintName))
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("drop constraint: %w", err)
	}

	return nil
}

// AddUniqueConstraint adds a UNIQUE constraint to a column.
func AddUniqueConstraint(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, column string) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(column, "column"); err != nil {
		return err
	}
	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}
	if err := ValidateColumns(ctx, pool, schemaName, tableName, []string{column}); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	constraintName := fmt.Sprintf("uq_%s_%s", tableName, column)
	sql := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s)", qt, quoteIdent(constraintName), quoteIdent(column))
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("add unique constraint: %w", err)
	}

	return nil
}

// CreateIndex creates an index on a column. If unique is true, creates a UNIQUE index.
func CreateIndex(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, column string, unique bool) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(column, "column"); err != nil {
		return err
	}
	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}
	if err := ValidateColumns(ctx, pool, schemaName, tableName, []string{column}); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	indexName := fmt.Sprintf("idx_%s_%s", tableName, column)
	uniqueKw := ""
	if unique {
		uniqueKw = "UNIQUE "
	}
	sql := fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", uniqueKw, quoteIdent(indexName), qt, quoteIdent(column))
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("create index: %w", err)
	}

	return nil
}

// DropIndex drops an index from the schema.
func DropIndex(ctx context.Context, pool *pgxpool.Pool, schemaName, indexName string) error {
	sql := fmt.Sprintf("DROP INDEX %s.%s", quoteIdent(schemaName), quoteIdent(indexName))
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("drop index: %w", err)
	}

	return nil
}

// DBFunction represents a PostgreSQL function in a schema.
type DBFunction struct {
	Name       string `json:"name"`
	Language   string `json:"language"`
	ReturnType string `json:"return_type"`
}

// ListFunctions returns all user-defined functions in the given schema.
func ListFunctions(ctx context.Context, pool *pgxpool.Pool, schemaName string) ([]DBFunction, error) {
	rows, err := pool.Query(ctx,
		`SELECT routine_name, COALESCE(external_language, 'sql'), data_type
		 FROM information_schema.routines
		 WHERE specific_schema = $1 AND routine_type = 'FUNCTION'
		 ORDER BY routine_name`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list functions: %w", err)
	}
	defer rows.Close()

	funcs := make([]DBFunction, 0)
	for rows.Next() {
		var f DBFunction
		if err := rows.Scan(&f.Name, &f.Language, &f.ReturnType); err != nil {
			return nil, fmt.Errorf("scan function: %w", err)
		}
		funcs = append(funcs, f)
	}
	return funcs, nil
}

// CreateFunctionRequest describes a zero-arg function to create.
type CreateFunctionRequest struct {
	Name     string `json:"name"`
	Body     string `json:"body"`
	Returns  string `json:"returns"`
	Language string `json:"language"`
}

// allowedFunctionLanguages are the languages permitted for user functions.
var allowedFunctionLanguages = map[string]bool{
	"sql":     true,
	"plpgsql": true,
}

// allowedReturnTypes are the return types permitted for user functions.
var allowedReturnTypes = map[string]bool{
	"void":    true,
	"text":    true,
	"integer": true,
	"bigint":  true,
	"boolean": true,
	"jsonb":   true,
	"json":    true,
	"numeric": true,
	"trigger": true,
}

// CreateFunction creates or replaces a zero-argument function in the schema.
func CreateFunction(ctx context.Context, pool *pgxpool.Pool, schemaName string, req CreateFunctionRequest) error {
	if err := validateIdentifier(req.Name, "function"); err != nil {
		return err
	}

	lang := strings.ToLower(strings.TrimSpace(req.Language))
	if lang == "" {
		lang = "plpgsql"
	}
	if !allowedFunctionLanguages[lang] {
		return fmt.Errorf("unsupported language %q; use 'sql' or 'plpgsql'", req.Language)
	}

	returns := strings.ToLower(strings.TrimSpace(req.Returns))
	if returns == "" {
		returns = "void"
	}
	if !allowedReturnTypes[returns] {
		return fmt.Errorf("unsupported return type %q", req.Returns)
	}

	if strings.TrimSpace(req.Body) == "" {
		return fmt.Errorf("function body cannot be empty")
	}

	// Reject dollar-quoting in body to prevent escaping out of the $$ block.
	if strings.Contains(req.Body, "$$") {
		return fmt.Errorf("function body cannot contain '$$'")
	}

	sql := fmt.Sprintf(
		"CREATE OR REPLACE FUNCTION %s.%s() RETURNS %s LANGUAGE %s AS $$ %s $$",
		quoteIdent(schemaName), quoteIdent(req.Name),
		returns, lang, req.Body,
	)

	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("create function: %w", err)
	}

	return nil
}

// DropFunction drops a zero-argument function from the schema.
func DropFunction(ctx context.Context, pool *pgxpool.Pool, schemaName, funcName string) error {
	if err := validateIdentifier(funcName, "function"); err != nil {
		return err
	}

	sql := fmt.Sprintf("DROP FUNCTION IF EXISTS %s.%s()", quoteIdent(schemaName), quoteIdent(funcName))
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("drop function: %w", err)
	}

	return nil
}

// AlterColumnDefault sets or drops the default value of a column.
func AlterColumnDefault(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, col string, defaultVal *string) error {
	if err := validateIdentifier(tableName, "table"); err != nil {
		return err
	}
	if err := validateIdentifier(col, "column"); err != nil {
		return err
	}

	if err := ValidateTable(ctx, pool, schemaName, tableName); err != nil {
		return err
	}
	if err := ValidateColumns(ctx, pool, schemaName, tableName, []string{col}); err != nil {
		return err
	}

	qt := qualifiedTable(schemaName, tableName)
	var sql string
	if defaultVal == nil {
		sql = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", qt, quoteIdent(col))
	} else {
		if err := validateDefault(*defaultVal); err != nil {
			return err
		}
		sql = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s", qt, quoteIdent(col), *defaultVal)
	}
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("alter column default: %w", err)
	}

	return nil
}

// RLSPolicy represents a PostgreSQL RLS policy.
type RLSPolicy struct {
	Name       string `json:"name"`
	Command    string `json:"command"`    // SELECT, INSERT, UPDATE, DELETE, ALL
	Permissive bool   `json:"permissive"` // true=PERMISSIVE, false=RESTRICTIVE
	Qual       string `json:"qual"`       // USING clause
	WithCheck  string `json:"with_check"` // WITH CHECK clause
}

// ListPolicies returns all RLS policies for a table.
func ListPolicies(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName string) ([]RLSPolicy, error) {
	rows, err := pool.Query(ctx,
		`SELECT polname, polcmd, polpermissive,
		        pg_get_expr(polqual, polrelid) AS qual,
		        pg_get_expr(polwithcheck, polrelid) AS with_check
		 FROM pg_policy
		 WHERE polrelid = (
		     SELECT oid FROM pg_class
		     WHERE relname = $1
		       AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = $2)
		 )
		 ORDER BY polname`,
		tableName, schemaName,
	)
	if err != nil {
		return nil, fmt.Errorf("query policies: %w", err)
	}
	defer rows.Close()

	cmdMap := map[byte]string{'r': "SELECT", 'a': "INSERT", 'w': "UPDATE", 'd': "DELETE", '*': "ALL"}

	policies := make([]RLSPolicy, 0)
	for rows.Next() {
		var p RLSPolicy
		var cmd byte
		var qual, withCheck *string
		if err := rows.Scan(&p.Name, &cmd, &p.Permissive, &qual, &withCheck); err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		p.Command = cmdMap[cmd]
		if p.Command == "" {
			p.Command = "ALL"
		}
		if qual != nil {
			p.Qual = *qual
		}
		if withCheck != nil {
			p.WithCheck = *withCheck
		}
		policies = append(policies, p)
	}
	return policies, nil
}

// ApplyPolicyPreset drops all existing policies and applies a preset.
func ApplyPolicyPreset(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, preset, userIDColumn string) error {
	qt := qualifiedTable(schemaName, tableName)

	// Drop all existing policies first.
	existing, err := ListPolicies(ctx, pool, schemaName, tableName)
	if err != nil {
		return err
	}
	for _, p := range existing {
		dropSQL := fmt.Sprintf("DROP POLICY %s ON %s", quoteIdent(p.Name), qt)
		if _, err := pool.Exec(ctx, dropSQL); err != nil {
			slog.Warn("failed to drop policy", "policy", p.Name, "error", err)
		}
	}

	if userIDColumn == "" {
		userIDColumn = "user_id"
	}
	col := quoteIdent(userIDColumn)

	// Set search_path so auth_uid() / auth_role() are found in the tenant schema.
	if _, err := pool.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", quoteIdent(schemaName))); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}
	defer pool.Exec(ctx, "SET search_path TO public") //nolint:errcheck

	var sqls []string
	switch preset {
	case "owner_access":
		sqls = []string{
			fmt.Sprintf("CREATE POLICY owner_select ON %s FOR SELECT USING (%s = auth_uid())", qt, col),
			fmt.Sprintf("CREATE POLICY owner_insert ON %s FOR INSERT WITH CHECK (%s = auth_uid())", qt, col),
			fmt.Sprintf("CREATE POLICY owner_update ON %s FOR UPDATE USING (%s = auth_uid())", qt, col),
			fmt.Sprintf("CREATE POLICY owner_delete ON %s FOR DELETE USING (%s = auth_uid())", qt, col),
		}
	case "public_read_owner_write":
		sqls = []string{
			fmt.Sprintf("CREATE POLICY public_select ON %s FOR SELECT USING (true)", qt),
			fmt.Sprintf("CREATE POLICY owner_insert ON %s FOR INSERT WITH CHECK (%s = auth_uid())", qt, col),
			fmt.Sprintf("CREATE POLICY owner_update ON %s FOR UPDATE USING (%s = auth_uid())", qt, col),
			fmt.Sprintf("CREATE POLICY owner_delete ON %s FOR DELETE USING (%s = auth_uid())", qt, col),
		}
	case "authenticated_read_owner_write":
		sqls = []string{
			fmt.Sprintf("CREATE POLICY auth_select ON %s FOR SELECT USING (auth_role() = 'authenticated')", qt),
			fmt.Sprintf("CREATE POLICY owner_insert ON %s FOR INSERT WITH CHECK (%s = auth_uid())", qt, col),
			fmt.Sprintf("CREATE POLICY owner_update ON %s FOR UPDATE USING (%s = auth_uid())", qt, col),
			fmt.Sprintf("CREATE POLICY owner_delete ON %s FOR DELETE USING (%s = auth_uid())", qt, col),
		}
	case "full_access":
		sqls = []string{
			fmt.Sprintf("CREATE POLICY allow_all ON %s FOR ALL USING (true)", qt),
		}
	case "read_only":
		sqls = []string{
			fmt.Sprintf("CREATE POLICY read_only ON %s FOR SELECT USING (true)", qt),
		}
	default:
		return fmt.Errorf("unknown policy preset: %s", preset)
	}

	for _, sql := range sqls {
		if _, err := pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("apply policy: %w", err)
		}
	}

	slog.Info("RLS policy preset applied", "schema", schemaName, "table", tableName, "preset", preset)
	return nil
}

// CreateCustomPolicy creates a single custom RLS policy.
func CreateCustomPolicy(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, policyName, command, usingExpr, withCheckExpr string) error {
	qt := qualifiedTable(schemaName, tableName)

	if !validIdentRe.MatchString(policyName) {
		return fmt.Errorf("invalid policy name (use letters, digits, underscores)")
	}

	validCmds := map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "ALL": true}
	command = strings.ToUpper(command)
	if !validCmds[command] {
		return fmt.Errorf("command must be SELECT, INSERT, UPDATE, DELETE, or ALL")
	}

	// Set search_path so auth_uid() / auth_role() are found in the tenant schema.
	if _, err := pool.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", quoteIdent(schemaName))); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}
	defer pool.Exec(ctx, "SET search_path TO public") //nolint:errcheck

	sql := fmt.Sprintf("CREATE POLICY %s ON %s FOR %s", quoteIdent(policyName), qt, command)
	if usingExpr != "" {
		sql += fmt.Sprintf(" USING (%s)", usingExpr)
	}
	if withCheckExpr != "" {
		sql += fmt.Sprintf(" WITH CHECK (%s)", withCheckExpr)
	}

	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("create policy: %w", err)
	}
	slog.Info("custom RLS policy created", "schema", schemaName, "table", tableName, "policy", policyName)
	return nil
}

// DropPolicy drops an RLS policy by name.
func DropPolicy(ctx context.Context, pool *pgxpool.Pool, schemaName, tableName, policyName string) error {
	qt := qualifiedTable(schemaName, tableName)
	sql := fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s", quoteIdent(policyName), qt)
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("drop policy: %w", err)
	}
	return nil
}
