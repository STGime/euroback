package query

import (
	"encoding/json"
	"log/slog"
	"net/http"

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

// HandleDDL returns a chi.Router for schema DDL operations.
// Mounted at /platform/projects/{id}/schema/tables
func HandleDDL(pool *pgxpool.Pool) chi.Router {
	r := chi.NewRouter()

	r.Post("/", handleCreateTable(pool))
	r.Delete("/{table}", handleDropTable(pool))
	r.Post("/{table}/columns", handleAddColumn(pool))
	r.Delete("/{table}/columns/{column}", handleDropColumn(pool))

	return r
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

		slog.Info("column dropped", "schema", schemaName, "table", tableName, "column", columnName)
		w.WriteHeader(http.StatusNoContent)
	}
}
