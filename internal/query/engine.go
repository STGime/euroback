package query

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryEngine translates REST API calls into parameterized PostgreSQL queries.
type QueryEngine struct {
	pool *pgxpool.Pool
}

// NewQueryEngine creates a new QueryEngine backed by the given connection pool.
func NewQueryEngine(pool *pgxpool.Pool) *QueryEngine {
	return &QueryEngine{pool: pool}
}


// withEndUserRLS wraps a function in a transaction with SET LOCAL app.end_user_id
// if an end-user ID is present in the context and the key type is not "secret".
func (e *QueryEngine) withEndUserRLS(ctx context.Context, fn func(ctx context.Context) error) error {
	endUserID := EndUserIDFromContext(ctx)
	keyType := KeyTypeFromContext(ctx)

	// Secret keys bypass RLS (service-level access).
	if keyType == "secret" || endUserID == "" {
		return fn(ctx)
	}

	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL app.end_user_id = '%s'", endUserID)); err != nil {
		return fmt.Errorf("set end_user_id: %w", err)
	}

	if err := fn(ctx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SelectRows builds and executes a parameterized SELECT query.
// It validates the table and columns against pg_catalog before executing.
// Returns the result rows, total count, and any error.
func (e *QueryEngine) SelectRows(ctx context.Context, schemaName, tableName string, params QueryParams) ([]map[string]interface{}, int, error) {
	// Validate table exists.
	if err := ValidateTable(ctx, e.pool, schemaName, tableName); err != nil {
		return nil, 0, err
	}

	// Validate selected columns.
	if len(params.Select) > 0 {
		if err := ValidateColumns(ctx, e.pool, schemaName, tableName, params.Select); err != nil {
			return nil, 0, err
		}
	}

	// Validate filter columns.
	filterCols := make([]string, 0, len(params.Filters))
	for _, f := range params.Filters {
		filterCols = append(filterCols, f.Column)
	}
	if len(filterCols) > 0 {
		if err := ValidateColumns(ctx, e.pool, schemaName, tableName, filterCols); err != nil {
			return nil, 0, err
		}
	}

	// Validate order columns.
	orderCols := make([]string, 0, len(params.OrderBy))
	for _, o := range params.OrderBy {
		orderCols = append(orderCols, o.Column)
	}
	if len(orderCols) > 0 {
		if err := ValidateColumns(ctx, e.pool, schemaName, tableName, orderCols); err != nil {
			return nil, 0, err
		}
	}

	// Enforce max limit.
	if params.Limit > 1000 {
		params.Limit = 1000
	}

	// Build the SELECT query.
	selectSQL, selectArgs := buildSelectQuery(schemaName, tableName, params)
	slog.Debug("executing select query", "sql", selectSQL, "args_count", len(selectArgs))

	// Build the COUNT query.
	countSQL, countArgs := buildCountQuery(schemaName, tableName, params)

	rows, err := e.pool.Query(ctx, selectSQL, selectArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("execute select: %w", err)
	}
	defer rows.Close()

	results, err := scanRows(rows)
	if err != nil {
		return nil, 0, err
	}

	var totalCount int
	err = e.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&totalCount)
	if err != nil {
		return nil, 0, fmt.Errorf("execute count: %w", err)
	}

	return results, totalCount, nil
}

// InsertRow builds and executes a parameterized INSERT ... RETURNING * query.
func (e *QueryEngine) InsertRow(ctx context.Context, schemaName, tableName string, data map[string]interface{}) (map[string]interface{}, error) {
	// Validate table exists.
	if err := ValidateTable(ctx, e.pool, schemaName, tableName); err != nil {
		return nil, err
	}

	var sql string
	var args []interface{}

	if len(data) == 0 {
		// All columns have defaults — use DEFAULT VALUES.
		qt := qualifiedTable(schemaName, tableName)
		sql = fmt.Sprintf("INSERT INTO %s DEFAULT VALUES RETURNING *", qt)
	} else {
		// Validate columns.
		cols := make([]string, 0, len(data))
		for k := range data {
			cols = append(cols, k)
		}
		if err := ValidateColumns(ctx, e.pool, schemaName, tableName, cols); err != nil {
			return nil, err
		}
		sql, args = buildInsertQuery(schemaName, tableName, data)
	}

	slog.Debug("executing insert query", "sql", sql, "args_count", len(args))

	rows, err := e.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("execute insert: %w", err)
	}
	defer rows.Close()

	results, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("insert returned no rows")
	}

	return results[0], nil
}

// UpdateRow builds and executes a parameterized UPDATE ... WHERE id = $1 RETURNING *.
func (e *QueryEngine) UpdateRow(ctx context.Context, schemaName, tableName, rowID string, data map[string]interface{}) (map[string]interface{}, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no data provided for update")
	}

	// Validate table exists.
	if err := ValidateTable(ctx, e.pool, schemaName, tableName); err != nil {
		return nil, err
	}

	// Validate columns.
	cols := make([]string, 0, len(data))
	for k := range data {
		cols = append(cols, k)
	}
	if err := ValidateColumns(ctx, e.pool, schemaName, tableName, cols); err != nil {
		return nil, err
	}

	sql, args := buildUpdateQuery(schemaName, tableName, rowID, data)
	slog.Debug("executing update query", "sql", sql, "args_count", len(args))

	rows, err := e.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("execute update: %w", err)
	}
	defer rows.Close()

	results, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("row not found: %s", rowID)
	}

	return results[0], nil
}

// DeleteRow builds and executes a parameterized DELETE WHERE id = $1.
func (e *QueryEngine) DeleteRow(ctx context.Context, schemaName, tableName, rowID string) error {
	// Validate table exists.
	if err := ValidateTable(ctx, e.pool, schemaName, tableName); err != nil {
		return err
	}

	sql, args := buildDeleteQuery(schemaName, tableName, rowID)
	slog.Debug("executing delete query", "sql", sql, "args_count", len(args))

	tag, err := e.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("execute delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("row not found: %s", rowID)
	}

	return nil
}

// CallFunction calls a PostgreSQL function via SELECT schema.func(args).
func (e *QueryEngine) CallFunction(ctx context.Context, schemaName, funcName string, args map[string]interface{}) (interface{}, error) {
	// Validate function name characters (alphanumeric + underscore only).
	for _, c := range funcName {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return nil, fmt.Errorf("invalid function name: %q", funcName)
		}
	}

	// Validate the function exists in the schema.
	var exists bool
	err := e.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.routines
			WHERE routine_schema = $1 AND routine_name = $2
		)`,
		schemaName, funcName,
	).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("check function existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("function %q does not exist in schema %q", funcName, schemaName)
	}

	// Build function call: SELECT "schema"."func"($1, $2, ...)
	paramPlaceholders := make([]string, 0, len(args))
	paramValues := make([]interface{}, 0, len(args))
	argIdx := 1
	// Build named parameter calls: func(param_name := $1, ...)
	namedParams := make([]string, 0, len(args))
	for name, val := range args {
		namedParams = append(namedParams, fmt.Sprintf("%s := $%d", quoteIdent(name), argIdx))
		paramValues = append(paramValues, val)
		argIdx++
	}
	_ = paramPlaceholders // unused in named mode

	var sql string
	if len(namedParams) > 0 {
		sql = fmt.Sprintf("SELECT %s.%s(%s)",
			quoteIdent(schemaName),
			quoteIdent(funcName),
			strings.Join(namedParams, ", "),
		)
	} else {
		sql = fmt.Sprintf("SELECT %s.%s()",
			quoteIdent(schemaName),
			quoteIdent(funcName),
		)
	}

	slog.Debug("executing function call", "sql", sql, "args_count", len(paramValues))

	var result interface{}
	err = e.pool.QueryRow(ctx, sql, paramValues...).Scan(&result)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("execute function: %w", err)
	}

	return result, nil
}

// ExecuteSQL runs a raw SQL query within the tenant's schema context.
// It uses a read-only transaction with search_path and statement_timeout.
func (e *QueryEngine) ExecuteSQL(ctx context.Context, schemaName, rawSQL string, maxRows int) ([]string, []map[string]interface{}, error) {
	// Strip trailing semicolon so the query can be wrapped in a subquery.
	rawSQL = strings.TrimSpace(rawSQL)
	rawSQL = strings.TrimRight(rawSQL, ";")
	rawSQL = strings.TrimSpace(rawSQL)

	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only tx, rollback is fine

	// Set transaction to read-only.
	if _, err := tx.Exec(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return nil, nil, fmt.Errorf("set read only: %w", err)
	}

	// Isolate to tenant schema.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", quoteIdent(schemaName))); err != nil {
		return nil, nil, fmt.Errorf("set search_path: %w", err)
	}

	// Prevent runaway queries.
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '10s'"); err != nil {
		return nil, nil, fmt.Errorf("set statement_timeout: %w", err)
	}

	// Execute the user's query with a row limit wrapper.
	wrappedSQL := fmt.Sprintf("SELECT * FROM (%s) AS _eurobase_q LIMIT %d", rawSQL, maxRows)
	rows, err := tx.Query(ctx, wrappedSQL)
	if err != nil {
		return nil, nil, fmt.Errorf("execute query: %w", err)
	}
	defer rows.Close()

	// Extract column names.
	fieldDescs := rows.FieldDescriptions()
	columns := make([]string, len(fieldDescs))
	for i, fd := range fieldDescs {
		columns[i] = fd.Name
	}

	results, err := scanRows(rows)
	if err != nil {
		return nil, nil, err
	}

	return columns, results, nil
}

// scanRows converts pgx rows into a slice of maps, normalizing types
// (e.g., UUIDs from [16]byte to string) for clean JSON serialization.
func scanRows(rows pgx.Rows) ([]map[string]interface{}, error) {
	fieldDescs := rows.FieldDescriptions()
	var results []map[string]interface{}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("read row values: %w", err)
		}

		row := make(map[string]interface{}, len(fieldDescs))
		for i, fd := range fieldDescs {
			row[fd.Name] = normalizeValue(values[i])
		}
		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return results, nil
}

// normalizeValue converts pgx-specific types to JSON-friendly representations.
func normalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case [16]byte:
		// UUID: format as standard string "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			val[0:4], val[4:6], val[6:8], val[8:10], val[10:16])
	default:
		return v
	}
}
