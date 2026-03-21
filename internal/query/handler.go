package query

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// jsonError writes a JSON error response with the given status code.
func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// jsonResponse writes a JSON response with the given status code.
func jsonResponse(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// HandleQuery returns a chi.Router that handles all /v1/db/ routes.
// Deprecated: prefer using the individual Handle* functions directly on
// the parent router so that explicit routes (like /sql) are not shadowed.
func HandleQuery(engine *QueryEngine) chi.Router {
	r := chi.NewRouter()

	r.Get("/{table}", handleSelectRows(engine))
	r.Get("/{table}/{id}", handleSelectRowByID(engine))
	r.Post("/{table}", handleInsertRow(engine))
	r.Patch("/{table}/{id}", handleUpdateRow(engine))
	r.Delete("/{table}/{id}", handleDeleteRow(engine))

	return r
}

// HandleTableGet returns the handler for GET /v1/db/{table}.
func HandleTableGet(engine *QueryEngine) http.HandlerFunc {
	return handleSelectRows(engine)
}

// HandleTableGetByID returns the handler for GET /v1/db/{table}/{id}.
func HandleTableGetByID(engine *QueryEngine) http.HandlerFunc {
	return handleSelectRowByID(engine)
}

// HandleTableInsert returns the handler for POST /v1/db/{table}.
func HandleTableInsert(engine *QueryEngine) http.HandlerFunc {
	return handleInsertRow(engine)
}

// HandleTableUpdate returns the handler for PATCH /v1/db/{table}/{id}.
func HandleTableUpdate(engine *QueryEngine) http.HandlerFunc {
	return handleUpdateRow(engine)
}

// HandleTableDelete returns the handler for DELETE /v1/db/{table}/{id}.
func HandleTableDelete(engine *QueryEngine) http.HandlerFunc {
	return handleDeleteRow(engine)
}

// HandleRPC returns an http.HandlerFunc for POST /v1/db/rpc/{function}.
func HandleRPC(engine *QueryEngine) chi.Router {
	r := chi.NewRouter()
	r.Post("/{function}", handleCallFunction(engine))
	return r
}

// HandleSchemaIntrospection returns an http.HandlerFunc for schema introspection.
func HandleSchemaIntrospection(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			jsonError(w, "project ID is required", http.StatusBadRequest)
			return
		}

		// Look up schema name for this project.
		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`,
			projectID,
		).Scan(&schemaName)
		if err != nil {
			slog.Error("failed to look up project schema", "error", err, "project_id", projectID)
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		// Get all tables in the schema.
		tables, err := GetSchemaTables(r.Context(), pool, schemaName)
		if err != nil {
			slog.Error("failed to list schema tables", "error", err, "schema", schemaName)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		// For each table, get column info.
		type TableSchema struct {
			Name    string       `json:"name"`
			Columns []ColumnInfo `json:"columns"`
		}

		result := make([]TableSchema, 0, len(tables))
		for _, t := range tables {
			cols, err := GetTableColumns(r.Context(), pool, schemaName, t)
			if err != nil {
				slog.Error("failed to get table columns", "error", err, "schema", schemaName, "table", t)
				jsonError(w, "internal server error", http.StatusInternalServerError)
				return
			}
			result = append(result, TableSchema{Name: t, Columns: cols})
		}

		slog.Debug("schema introspection complete", "project_id", projectID, "schema", schemaName, "table_count", len(result))
		jsonResponse(w, result, http.StatusOK)
	}
}

// handleSelectRows handles GET /v1/db/{table}.
func handleSelectRows(engine *QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		tableName := chi.URLParam(r, "table")
		if tableName == "" {
			jsonError(w, "table name is required", http.StatusBadRequest)
			return
		}

		params := ParseQueryParams(r)

		rows, totalCount, err := engine.SelectRows(r.Context(), schema, tableName, params)
		if err != nil {
			slog.Error("select query failed", "error", err, "schema", schema, "table", tableName)
			if !handleQueryError(w, err) {
				jsonError(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}

		if rows == nil {
			rows = make([]map[string]interface{}, 0)
		}

		resp := map[string]interface{}{
			"data":  rows,
			"count": totalCount,
		}

		slog.Debug("select query complete", "schema", schema, "table", tableName, "rows", len(rows), "total", totalCount)
		jsonResponse(w, resp, http.StatusOK)
	}
}

// handleSelectRowByID handles GET /v1/db/{table}/{id}.
func handleSelectRowByID(engine *QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		tableName := chi.URLParam(r, "table")
		rowID := chi.URLParam(r, "id")
		if tableName == "" || rowID == "" {
			jsonError(w, "table name and row ID are required", http.StatusBadRequest)
			return
		}

		params := ParseQueryParams(r)
		params.Filters = append(params.Filters, Filter{
			Column:   "id",
			Operator: "eq",
			Value:    rowID,
		})
		params.Limit = 1
		params.Offset = 0

		rows, _, err := engine.SelectRows(r.Context(), schema, tableName, params)
		if err != nil {
			slog.Error("select by id failed", "error", err, "schema", schema, "table", tableName, "id", rowID)
			if !handleQueryError(w, err) {
				jsonError(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}

		if len(rows) == 0 {
			jsonError(w, "row not found", http.StatusNotFound)
			return
		}

		jsonResponse(w, rows[0], http.StatusOK)
	}
}

// handleInsertRow handles POST /v1/db/{table}.
func handleInsertRow(engine *QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		tableName := chi.URLParam(r, "table")
		if tableName == "" {
			jsonError(w, "table name is required", http.StatusBadRequest)
			return
		}

		var data map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		row, err := engine.InsertRow(r.Context(), schema, tableName, data)
		if err != nil {
			slog.Error("insert failed", "error", err, "schema", schema, "table", tableName)
			if !handleQueryError(w, err) {
				jsonError(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}

		slog.Debug("row inserted", "schema", schema, "table", tableName)
		jsonResponse(w, row, http.StatusCreated)
	}
}

// handleUpdateRow handles PATCH /v1/db/{table}/{id}.
func handleUpdateRow(engine *QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		tableName := chi.URLParam(r, "table")
		rowID := chi.URLParam(r, "id")
		if tableName == "" || rowID == "" {
			jsonError(w, "table name and row ID are required", http.StatusBadRequest)
			return
		}

		var data map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		row, err := engine.UpdateRow(r.Context(), schema, tableName, rowID, data)
		if err != nil {
			slog.Error("update failed", "error", err, "schema", schema, "table", tableName, "id", rowID)
			if !handleQueryError(w, err) {
				jsonError(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}

		slog.Debug("row updated", "schema", schema, "table", tableName, "id", rowID)
		jsonResponse(w, row, http.StatusOK)
	}
}

// handleDeleteRow handles DELETE /v1/db/{table}/{id}.
func handleDeleteRow(engine *QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		tableName := chi.URLParam(r, "table")
		rowID := chi.URLParam(r, "id")
		if tableName == "" || rowID == "" {
			jsonError(w, "table name and row ID are required", http.StatusBadRequest)
			return
		}

		err := engine.DeleteRow(r.Context(), schema, tableName, rowID)
		if err != nil {
			slog.Error("delete failed", "error", err, "schema", schema, "table", tableName, "id", rowID)
			if !handleQueryError(w, err) {
				jsonError(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}

		slog.Debug("row deleted", "schema", schema, "table", tableName, "id", rowID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleCallFunction handles POST /v1/db/rpc/{function}.
func handleCallFunction(engine *QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		funcName := chi.URLParam(r, "function")
		if funcName == "" {
			jsonError(w, "function name is required", http.StatusBadRequest)
			return
		}

		var args map[string]interface{}
		if r.Body != nil && r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
				jsonError(w, "invalid request body", http.StatusBadRequest)
				return
			}
		}
		if args == nil {
			args = make(map[string]interface{})
		}

		result, err := engine.CallFunction(r.Context(), schema, funcName, args)
		if err != nil {
			slog.Error("rpc call failed", "error", err, "schema", schema, "function", funcName)
			if isNotFoundError(err) {
				jsonError(w, err.Error(), http.StatusNotFound)
				return
			}
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		jsonResponse(w, map[string]interface{}{"result": result}, http.StatusOK)
	}
}

// handleQueryError writes the appropriate HTTP error response for a query engine error.
// Returns true if it handled the error, false if the caller should use a generic 500.
func handleQueryError(w http.ResponseWriter, err error) bool {
	if isUniqueViolation(err) {
		field := extractConflictField(err)
		msg := "a record with this value already exists"
		if field != "" {
			msg = "a record with this " + field + " already exists"
		}
		jsonError(w, msg, http.StatusConflict)
		return true
	}
	if isTypeError(err) {
		jsonError(w, extractTypeErrorMessage(err), http.StatusBadRequest)
		return true
	}
	if isConstraintViolation(err) {
		jsonError(w, extractConstraintMessage(err), http.StatusBadRequest)
		return true
	}
	if isNotFoundError(err) {
		jsonError(w, err.Error(), http.StatusNotFound)
		return true
	}
	if isValidationError(err) {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return true
	}
	return false
}

// isNotFoundError checks if the error message indicates a not-found condition.
func isNotFoundError(err error) bool {
	msg := err.Error()
	return contains(msg, "not found") || contains(msg, "does not exist")
}

// isValidationError checks if the error message indicates a validation failure.
func isValidationError(err error) bool {
	msg := err.Error()
	return contains(msg, "invalid column") || contains(msg, "no data provided")
}

// isTypeError checks if the error is a PostgreSQL data type or syntax error (22xxx).
func isTypeError(err error) bool {
	msg := err.Error()
	return contains(msg, "SQLSTATE 22") || contains(msg, "invalid input syntax") || contains(msg, "invalid input value") || contains(msg, "out of range")
}

// extractTypeErrorMessage builds a user-friendly message from a PostgreSQL type error.
func extractTypeErrorMessage(err error) string {
	msg := err.Error()
	// "invalid input syntax for type bigint" → "invalid value for type bigint"
	if idx := strings.Index(msg, "invalid input syntax for type "); idx >= 0 {
		rest := msg[idx+len("invalid input syntax for type "):]
		if end := strings.IndexByte(rest, ' '); end >= 0 {
			return "invalid value for type " + rest[:end]
		}
		// Trim trailing quote/paren
		typeName := strings.TrimRight(rest, "\" ()")
		return "invalid value for type " + typeName
	}
	if contains(msg, "out of range") {
		return "value is out of range for this column type"
	}
	return "invalid value for this column type"
}

// isConstraintViolation checks for foreign key (23503) or not-null (23502) violations.
func isConstraintViolation(err error) bool {
	msg := err.Error()
	return contains(msg, "SQLSTATE 23503") || contains(msg, "SQLSTATE 23502") || contains(msg, "violates not-null") || contains(msg, "violates foreign key")
}

// extractConstraintMessage builds a user-friendly message from a constraint error.
func extractConstraintMessage(err error) string {
	msg := err.Error()
	if contains(msg, "violates not-null") || contains(msg, "SQLSTATE 23502") {
		field := extractConflictField(err)
		if field != "" {
			return field + " cannot be null"
		}
		return "a required field is missing"
	}
	if contains(msg, "violates foreign key") || contains(msg, "SQLSTATE 23503") {
		return "referenced record does not exist"
	}
	return "constraint violation"
}

// isUniqueViolation checks if the error is a PostgreSQL unique constraint violation (23505).
func isUniqueViolation(err error) bool {
	msg := err.Error()
	return contains(msg, "SQLSTATE 23505") || contains(msg, "duplicate key") || contains(msg, "unique constraint")
}

// extractConflictField tries to extract the column name from a unique violation error.
func extractConflictField(err error) string {
	msg := err.Error()
	// PostgreSQL error format: "Key (email)=(alice@example.com) already exists"
	if idx := strings.Index(msg, "Key ("); idx >= 0 {
		rest := msg[idx+5:]
		if end := strings.Index(rest, ")"); end >= 0 {
			return rest[:end]
		}
	}
	return ""
}

// contains is a case-insensitive substring check.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsLower(s, substr)
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
