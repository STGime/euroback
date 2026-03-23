package query

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateTableRequest is the JSON body for POST /platform/projects/{id}/schema/tables.
type CreateTableRequest struct {
	Name    string             `json:"name"`
	Columns []ColumnDefinition `json:"columns"`
}

// AddColumnRequest is the JSON body for POST .../tables/{table}/columns.
type AddColumnRequest struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	DefaultValue string `json:"default_value,omitempty"`
}

// SchemaChange represents a logged DDL operation.
type SchemaChange struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Action     string    `json:"action"`
	TableName  string    `json:"table_name"`
	ColumnName *string   `json:"column_name"`
	Detail     any       `json:"detail"`
	SQLText    *string   `json:"sql_text"`
	CreatedAt  time.Time `json:"created_at"`
}

// RenameTableRequest is the JSON body for PATCH /schema/tables/{table}.
type RenameTableRequest struct {
	NewName string `json:"new_name"`
}

// AlterColumnRequest is the JSON body for PATCH /schema/tables/{table}/columns/{column}.
type AlterColumnRequest struct {
	NewName      *string `json:"new_name,omitempty"`
	NewType      *string `json:"new_type,omitempty"`
	Nullable     *bool   `json:"nullable,omitempty"`
	DefaultValue *string `json:"default_value,omitempty"`
	DropDefault  bool    `json:"drop_default,omitempty"`
}

// HandleDDL returns a chi.Router for schema DDL operations.
// Mounted at /platform/projects/{id}/schema/tables
func HandleDDL(pool *pgxpool.Pool) chi.Router {
	r := chi.NewRouter()

	r.Post("/", handleCreateTable(pool))
	r.Delete("/{table}", handleDropTable(pool))
	r.Patch("/{table}", handleRenameTable(pool))
	r.Post("/{table}/columns", handleAddColumn(pool))
	r.Delete("/{table}/columns/{column}", handleDropColumn(pool))
	r.Patch("/{table}/columns/{column}", handleAlterColumn(pool))

	return r
}

// HandleSchemaChanges returns a handler that lists schema change history.
// GET /platform/projects/{id}/schema/changes
func HandleSchemaChanges(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		rows, err := pool.Query(r.Context(),
			`SELECT id, project_id, action, table_name, column_name, detail, sql_text, created_at
			 FROM schema_changes WHERE project_id = $1 ORDER BY created_at DESC LIMIT 100`, projectID)
		if err != nil {
			slog.Error("list schema changes failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		changes := make([]SchemaChange, 0)
		for rows.Next() {
			var c SchemaChange
			if err := rows.Scan(&c.ID, &c.ProjectID, &c.Action, &c.TableName, &c.ColumnName, &c.Detail, &c.SQLText, &c.CreatedAt); err != nil {
				slog.Error("scan schema change failed", "error", err)
				jsonError(w, "internal server error", http.StatusInternalServerError)
				return
			}
			changes = append(changes, c)
		}

		jsonResponse(w, changes, http.StatusOK)
	}
}

// logSchemaChange records a DDL operation in the schema_changes table.
func logSchemaChange(pool *pgxpool.Pool, r *http.Request, projectID, action, tableName string, columnName *string, detail any) {
	detailJSON, _ := json.Marshal(detail)
	_, err := pool.Exec(r.Context(),
		`INSERT INTO schema_changes (project_id, action, table_name, column_name, detail)
		 VALUES ($1, $2, $3, $4, $5)`,
		projectID, action, tableName, columnName, detailJSON,
	)
	if err != nil {
		slog.Error("failed to log schema change", "error", err, "action", action, "table", tableName)
	}
}

func handleCreateTable(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			jsonError(w, "project ID is required", http.StatusBadRequest)
			return
		}

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

		var req CreateTableRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if req.Name == "" {
			jsonError(w, "table name is required", http.StatusBadRequest)
			return
		}
		if len(req.Columns) == 0 {
			jsonError(w, "at least one column is required", http.StatusBadRequest)
			return
		}

		if err := CreateTable(r.Context(), pool, schemaName, req.Name, req.Columns); err != nil {
			slog.Error("create table failed", "error", err, "schema", schemaName, "table", req.Name)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "create_table", req.Name, nil, map[string]any{
			"columns": req.Columns,
		})

		slog.Info("table created", "schema", schemaName, "table", req.Name, "columns", len(req.Columns))
		jsonResponse(w, map[string]string{
			"status": "created",
			"table":  req.Name,
		}, http.StatusCreated)
	}
}

func handleDropTable(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		if projectID == "" || tableName == "" {
			jsonError(w, "project ID and table name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`,
			projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		if err := DropTable(r.Context(), pool, schemaName, tableName); err != nil {
			slog.Error("drop table failed", "error", err, "schema", schemaName, "table", tableName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "drop_table", tableName, nil, nil)

		slog.Info("table dropped", "schema", schemaName, "table", tableName)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleAddColumn(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		if projectID == "" || tableName == "" {
			jsonError(w, "project ID and table name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`,
			projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var req AddColumnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		col := ColumnDefinition{
			Name:         req.Name,
			Type:         req.Type,
			Nullable:     req.Nullable,
			DefaultValue: req.DefaultValue,
		}

		if err := AddColumn(r.Context(), pool, schemaName, tableName, col); err != nil {
			slog.Error("add column failed", "error", err, "schema", schemaName, "table", tableName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "add_column", tableName, &req.Name, map[string]any{
			"type":     req.Type,
			"nullable": req.Nullable,
			"default":  req.DefaultValue,
		})

		slog.Info("column added", "schema", schemaName, "table", tableName, "column", req.Name)
		jsonResponse(w, map[string]string{
			"status": "added",
			"column": req.Name,
		}, http.StatusCreated)
	}
}

func handleDropColumn(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")
		columnName := chi.URLParam(r, "column")

		if projectID == "" || tableName == "" || columnName == "" {
			jsonError(w, "project ID, table name, and column name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`,
			projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		if err := DropColumn(r.Context(), pool, schemaName, tableName, columnName); err != nil {
			slog.Error("drop column failed", "error", err, "schema", schemaName, "table", tableName, "column", columnName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "drop_column", tableName, &columnName, nil)

		slog.Info("column dropped", "schema", schemaName, "table", tableName, "column", columnName)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleRenameTable(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		if projectID == "" || tableName == "" {
			jsonError(w, "project ID and table name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`,
			projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var req RenameTableRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.NewName == "" {
			jsonError(w, "new_name is required", http.StatusBadRequest)
			return
		}

		if err := RenameTable(r.Context(), pool, schemaName, tableName, req.NewName); err != nil {
			slog.Error("rename table failed", "error", err, "schema", schemaName, "table", tableName, "new_name", req.NewName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "rename_table", req.NewName, nil, map[string]any{
			"old_name": tableName,
			"new_name": req.NewName,
		})

		slog.Info("table renamed", "schema", schemaName, "old_name", tableName, "new_name", req.NewName)
		jsonResponse(w, map[string]string{
			"status":   "renamed",
			"old_name": tableName,
			"new_name": req.NewName,
		}, http.StatusOK)
	}
}

func handleAlterColumn(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")
		columnName := chi.URLParam(r, "column")

		if projectID == "" || tableName == "" || columnName == "" {
			jsonError(w, "project ID, table name, and column name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`,
			projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var req AlterColumnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Track the current column name (may change if renamed).
		currentCol := columnName
		changes := map[string]any{}

		// Apply changes in sequence within a transaction.
		tx, err := pool.Begin(r.Context())
		if err != nil {
			slog.Error("begin transaction failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context()) //nolint:errcheck

		if req.NewType != nil {
			if err := AlterColumnType(r.Context(), pool, schemaName, tableName, currentCol, *req.NewType); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			changes["new_type"] = *req.NewType
		}

		if req.Nullable != nil {
			if err := AlterColumnNullable(r.Context(), pool, schemaName, tableName, currentCol, *req.Nullable); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			changes["nullable"] = *req.Nullable
		}

		if req.DropDefault {
			if err := AlterColumnDefault(r.Context(), pool, schemaName, tableName, currentCol, nil); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			changes["drop_default"] = true
		} else if req.DefaultValue != nil {
			if err := AlterColumnDefault(r.Context(), pool, schemaName, tableName, currentCol, req.DefaultValue); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			changes["default_value"] = *req.DefaultValue
		}

		if req.NewName != nil {
			if err := RenameColumn(r.Context(), pool, schemaName, tableName, currentCol, *req.NewName); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			changes["new_name"] = *req.NewName
			currentCol = *req.NewName
		}

		if err := tx.Commit(r.Context()); err != nil {
			slog.Error("commit failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		logSchemaChange(pool, r, projectID, "alter_column", tableName, &columnName, changes)

		slog.Info("column altered", "schema", schemaName, "table", tableName, "column", columnName, "changes", changes)
		jsonResponse(w, map[string]any{
			"status":  "altered",
			"column":  currentCol,
			"changes": changes,
		}, http.StatusOK)
	}
}
