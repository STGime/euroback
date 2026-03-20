package query

import (
	"fmt"
	"strings"
)

// quoteIdent safely quotes a SQL identifier using double quotes.
// Any embedded double quotes are doubled per SQL standard.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// qualifiedTable returns a fully-qualified, double-quoted "schema"."table" reference.
func qualifiedTable(schemaName, tableName string) string {
	return quoteIdent(schemaName) + "." + quoteIdent(tableName)
}

// buildSelectQuery builds a parameterized SELECT query from the given parameters.
// Returns the SQL string and the ordered parameter values.
func buildSelectQuery(schemaName, tableName string, params QueryParams) (string, []interface{}) {
	var args []interface{}
	argIdx := 1

	// Columns.
	colExpr := "*"
	if len(params.Select) > 0 {
		quoted := make([]string, len(params.Select))
		for i, c := range params.Select {
			quoted[i] = quoteIdent(c)
		}
		colExpr = strings.Join(quoted, ", ")
	}

	qt := qualifiedTable(schemaName, tableName)

	// WHERE clause.
	var whereClauses []string
	for _, f := range params.Filters {
		clause, newArgs, newIdx := buildFilterClause(f, argIdx)
		if clause != "" {
			whereClauses = append(whereClauses, clause)
			args = append(args, newArgs...)
			argIdx = newIdx
		}
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// ORDER BY clause.
	orderSQL := ""
	if len(params.OrderBy) > 0 {
		parts := make([]string, len(params.OrderBy))
		for i, o := range params.OrderBy {
			dir := "ASC"
			if o.Descending {
				dir = "DESC"
			}
			parts[i] = quoteIdent(o.Column) + " " + dir
		}
		orderSQL = " ORDER BY " + strings.Join(parts, ", ")
	}

	// LIMIT and OFFSET.
	limitSQL := fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.Offset)

	sql := fmt.Sprintf("SELECT %s FROM %s%s%s%s", colExpr, qt, whereSQL, orderSQL, limitSQL)
	return sql, args
}

// buildCountQuery builds a SELECT count(*) query with the same filters as a select.
func buildCountQuery(schemaName, tableName string, params QueryParams) (string, []interface{}) {
	var args []interface{}
	argIdx := 1

	qt := qualifiedTable(schemaName, tableName)

	var whereClauses []string
	for _, f := range params.Filters {
		clause, newArgs, newIdx := buildFilterClause(f, argIdx)
		if clause != "" {
			whereClauses = append(whereClauses, clause)
			args = append(args, newArgs...)
			argIdx = newIdx
		}
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	sql := fmt.Sprintf("SELECT count(*) FROM %s%s", qt, whereSQL)
	return sql, args
}

// buildFilterClause builds a single WHERE clause for the given filter.
// Returns the SQL fragment, parameter values, and the next argument index.
func buildFilterClause(f Filter, argIdx int) (string, []interface{}, int) {
	col := quoteIdent(f.Column)
	sqlOp := SQLOperator(f.Operator)

	switch f.Operator {
	case "is":
		// IS NULL / IS NOT NULL — no parameterized value.
		val := strings.ToUpper(strings.TrimSpace(f.Value))
		switch val {
		case "NULL":
			return fmt.Sprintf("%s IS NULL", col), nil, argIdx
		case "TRUE":
			return fmt.Sprintf("%s IS TRUE", col), nil, argIdx
		case "FALSE":
			return fmt.Sprintf("%s IS FALSE", col), nil, argIdx
		default:
			// Unsupported IS value; skip.
			return "", nil, argIdx
		}

	case "in":
		// IN clause: split comma-separated values into individual parameters.
		values := strings.Split(f.Value, ",")
		placeholders := make([]string, len(values))
		args := make([]interface{}, len(values))
		for i, v := range values {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args[i] = strings.TrimSpace(v)
			argIdx++
		}
		clause := fmt.Sprintf("%s IN (%s)", col, strings.Join(placeholders, ", "))
		return clause, args, argIdx

	default:
		clause := fmt.Sprintf("%s %s $%d", col, sqlOp, argIdx)
		return clause, []interface{}{f.Value}, argIdx + 1
	}
}

// buildInsertQuery builds a parameterized INSERT ... RETURNING * query.
func buildInsertQuery(schemaName, tableName string, data map[string]interface{}) (string, []interface{}) {
	qt := qualifiedTable(schemaName, tableName)

	columns := make([]string, 0, len(data))
	placeholders := make([]string, 0, len(data))
	args := make([]interface{}, 0, len(data))
	argIdx := 1

	for col, val := range data {
		columns = append(columns, quoteIdent(col))
		placeholders = append(placeholders, fmt.Sprintf("$%d", argIdx))
		args = append(args, val)
		argIdx++
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING *",
		qt,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
	)

	return sql, args
}

// buildUpdateQuery builds a parameterized UPDATE ... WHERE id = $1 RETURNING * query.
func buildUpdateQuery(schemaName, tableName, rowID string, data map[string]interface{}) (string, []interface{}) {
	qt := qualifiedTable(schemaName, tableName)

	// $1 is always the row ID.
	args := []interface{}{rowID}
	argIdx := 2

	setClauses := make([]string, 0, len(data))
	for col, val := range data {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", quoteIdent(col), argIdx))
		args = append(args, val)
		argIdx++
	}

	sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s = $1 RETURNING *",
		qt,
		strings.Join(setClauses, ", "),
		quoteIdent("id"),
	)

	return sql, args
}

// buildDeleteQuery builds a parameterized DELETE WHERE id = $1 query.
func buildDeleteQuery(schemaName, tableName, rowID string) (string, []interface{}) {
	qt := qualifiedTable(schemaName, tableName)
	sql := fmt.Sprintf("DELETE FROM %s WHERE %s = $1", qt, quoteIdent("id"))
	return sql, []interface{}{rowID}
}
