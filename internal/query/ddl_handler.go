package query

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateTableRequest is the JSON body for POST /platform/projects/{id}/schema/tables.
//
// RLSPreset optionally applies an RLS policy preset right after table
// creation. If left empty, the handler auto-detects an owner-style column
// (user_id / owner_id / created_by) and applies the "owner_access" preset
// against it. If no such column exists, the table is created with RLS
// enabled but no policies (deny-all to end-users) — safe by default.
// Pass "none" to explicitly skip the auto-preset behaviour.
//
// DisableRLS explicitly turns RLS OFF after creation. This is an opt-out
// for developers who know they want a public table with no row-level
// filtering. The response includes a top-level "warning" when this is set
// so the client (console or SDK) can surface the risk.
type CreateTableRequest struct {
	Name           string             `json:"name"`
	Columns        []ColumnDefinition `json:"columns"`
	RLSPreset      string             `json:"rls_preset,omitempty"`
	RLSUserIDColum string             `json:"rls_user_id_column,omitempty"`
	DisableRLS     bool               `json:"disable_rls,omitempty"`
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

	// Foreign keys
	r.Post("/{table}/foreign-keys", handleAddForeignKey(pool))
	r.Delete("/{table}/constraints/{constraint}", handleDropConstraint(pool))

	// Unique constraints
	r.Post("/{table}/constraints/unique", handleAddUniqueConstraint(pool))

	// Indexes
	r.Get("/{table}/indexes", handleListIndexes(pool))
	r.Post("/{table}/indexes", handleCreateIndex(pool))
	r.Delete("/{table}/indexes/{index}", handleDropIndex(pool))

	// RLS
	r.Post("/{table}/rls", handleToggleRLS(pool))
	r.Get("/{table}/policies", handleListPolicies(pool))
	r.Post("/{table}/policies", handleCreatePolicy(pool))
	r.Post("/{table}/policies/preset", handleApplyPreset(pool))
	r.Delete("/{table}/policies/{policy}", handleDropPolicy(pool))

	return r
}

// HandleRLSAudit returns a handler for GET /platform/projects/{id}/schema/rls-audit.
// It reports every user-facing table's RLS posture so the console can warn
// developers about tables missing RLS or policies — the "silent multi-tenant
// data leak" class of bug.
func HandleRLSAudit(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		entries, err := AuditRLS(r.Context(), pool, schemaName)
		if err != nil {
			slog.Error("rls audit failed", "error", err, "schema", schemaName)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		warnings := 0
		critical := 0
		for _, e := range entries {
			switch e.Severity {
			case "warning":
				warnings++
			case "critical":
				critical++
			}
		}

		jsonResponse(w, map[string]any{
			"entries":        entries,
			"warning_count":  warnings,
			"critical_count": critical,
		}, http.StatusOK)
	}
}

// HandleSchemaChanges returns a handler that lists schema change history.
// GET /platform/projects/{id}/schema/changes
//
// On each request it also backfills any tables that exist in the tenant
// schema but have no "create_table" entry — this catches tables created
// via CLI migrations, psql, or any path that bypasses the DDL handlers.
func HandleSchemaChanges(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		// Resolve tenant schema for backfill.
		var schemaName string
		_ = pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName)

		// Backfill: find tables in the schema that have no create_table log entry.
		if schemaName != "" {
			backfillUnloggedTables(r.Context(), pool, projectID, schemaName)
		}

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

// platformTables are managed by the platform and excluded from schema change tracking.
var platformTables = map[string]bool{
	"users": true, "refresh_tokens": true, "email_tokens": true,
	"storage_objects": true, "user_identities": true, "todos": true,
}

// backfillUnloggedTables discovers tables in the tenant schema that have no
// corresponding "create_table" entry in schema_changes and inserts one.
func backfillUnloggedTables(ctx context.Context, pool *pgxpool.Pool, projectID, schemaName string) {
	rows, err := pool.Query(ctx,
		`SELECT t.table_name
		 FROM information_schema.tables t
		 WHERE t.table_schema = $1
		   AND t.table_type = 'BASE TABLE'
		   AND NOT EXISTS (
		       SELECT 1 FROM schema_changes sc
		       WHERE sc.project_id = $2
		         AND sc.table_name = t.table_name
		         AND sc.action = 'create_table'
		   )`, schemaName, projectID)
	if err != nil {
		slog.Debug("backfill schema changes: query failed", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			continue
		}
		if platformTables[tableName] {
			continue
		}
		// Insert a backfilled create_table entry.
		_, _ = pool.Exec(ctx,
			`INSERT INTO schema_changes (project_id, action, table_name, detail)
			 VALUES ($1, 'create_table', $2, '{"source":"backfill"}'::jsonb)`,
			projectID, tableName)
		slog.Info("backfilled schema change", "project_id", projectID, "table", tableName)
	}
}

// logSchemaChange records a DDL operation in the schema_changes table and
// emits an audit log entry (if the audit service is available in context).
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

	// Also write to the audit log so DDL shows up alongside other actions.
	// Note: we can't import internal/auth here (cycle), so we extract the
	// actor from the audit-compatible context helper instead. The platform
	// auth middleware already ran and the audit middleware injected the
	// service. If neither is available, we log with empty actor fields —
	// better than skipping the entry entirely.
	if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
		actorID, actorEmail := audit.ActorFromContext(r.Context())
		meta := map[string]interface{}{"table": tableName}
		if columnName != nil {
			meta["column"] = *columnName
		}
		if detail != nil {
			meta["detail"] = detail
		}
		auditSvc.Log(r.Context(), projectID, actorID, actorEmail,
			"schema."+action,
			audit.WithTarget("table", tableName),
			audit.WithMetadata(meta),
			audit.WithIP(r.RemoteAddr))
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

		// Apply a default RLS policy preset. RLS itself is already enabled by
		// CreateTable; this adds a policy so end-users can actually access
		// the table under the expected ownership model. The request-supplied
		// preset wins; otherwise we auto-detect a user-identifier column and
		// apply owner_access; otherwise we leave the table policy-less
		// (deny-all to end-users — safe, but opaque to clients until the
		// developer picks a preset). "none" explicitly skips auto-preset.
		appliedPreset := ""
		if req.RLSPreset != "" && req.RLSPreset != "none" {
			userIDCol := req.RLSUserIDColum
			if userIDCol == "" {
				userIDCol = detectOwnerColumn(req.Columns)
			}
			if userIDCol == "" {
				userIDCol = "user_id"
			}
			if err := ApplyPolicyPreset(r.Context(), pool, schemaName, req.Name, req.RLSPreset, userIDCol); err != nil {
				slog.Warn("apply default rls preset failed", "error", err, "table", req.Name, "preset", req.RLSPreset)
			} else {
				appliedPreset = req.RLSPreset
			}
		} else if req.RLSPreset == "" {
			if owner := detectOwnerColumn(req.Columns); owner != "" {
				if err := ApplyPolicyPreset(r.Context(), pool, schemaName, req.Name, "owner_access", owner); err != nil {
					slog.Warn("auto-apply owner_access preset failed", "error", err, "table", req.Name, "owner_column", owner)
				} else {
					appliedPreset = "owner_access"
				}
			}
		}

		var warning string
		if req.DisableRLS {
			qt := qualifiedTable(schemaName, req.Name)
			if _, err := pool.Exec(r.Context(), fmt.Sprintf("ALTER TABLE %s DISABLE ROW LEVEL SECURITY", qt)); err != nil {
				slog.Error("disable rls failed", "error", err, "table", req.Name)
				jsonError(w, "failed to disable rls: "+err.Error(), http.StatusInternalServerError)
				return
			}
			appliedPreset = ""
			warning = "RLS is DISABLED on this table — every authenticated request (and potentially anonymous ones depending on GRANTs) can read and write every row. Only do this for genuinely public data."
			slog.Warn("table created with RLS disabled (explicit opt-out)", "schema", schemaName, "table", req.Name)
		}

		logSchemaChange(pool, r, projectID, "create_table", req.Name, nil, map[string]any{
			"columns":     req.Columns,
			"rls_preset":  appliedPreset,
			"rls_enabled": !req.DisableRLS,
		})

		slog.Info("table created", "schema", schemaName, "table", req.Name, "columns", len(req.Columns), "rls_preset", appliedPreset, "rls_enabled", !req.DisableRLS)
		resp := map[string]any{
			"status":      "created",
			"table":       req.Name,
			"rls_preset":  appliedPreset,
			"rls_enabled": !req.DisableRLS,
		}
		if warning != "" {
			resp["warning"] = warning
		}
		jsonResponse(w, resp, http.StatusCreated)
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

func handleAddForeignKey(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		if projectID == "" || tableName == "" {
			jsonError(w, "project ID and table name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var req ForeignKeyDefinition
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := AddForeignKey(r.Context(), pool, schemaName, tableName, req); err != nil {
			slog.Error("add foreign key failed", "error", err, "schema", schemaName, "table", tableName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		colName := req.Column
		logSchemaChange(pool, r, projectID, "add_foreign_key", tableName, &colName, map[string]any{
			"referenced_table":  req.ReferencedTable,
			"referenced_column": req.ReferencedColumn,
			"on_delete":         req.OnDelete,
		})

		slog.Info("foreign key added", "schema", schemaName, "table", tableName, "column", req.Column)
		jsonResponse(w, map[string]string{"status": "created", "constraint": "fk_" + tableName + "_" + req.Column}, http.StatusCreated)
	}
}

func handleDropConstraint(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")
		constraintName := chi.URLParam(r, "constraint")

		if projectID == "" || tableName == "" || constraintName == "" {
			jsonError(w, "project ID, table name, and constraint name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		if err := DropConstraint(r.Context(), pool, schemaName, tableName, constraintName); err != nil {
			slog.Error("drop constraint failed", "error", err, "schema", schemaName, "table", tableName, "constraint", constraintName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		action := "drop_foreign_key"
		if len(constraintName) > 3 && constraintName[:3] == "uq_" {
			action = "drop_unique_constraint"
		}
		logSchemaChange(pool, r, projectID, action, tableName, nil, map[string]any{
			"constraint": constraintName,
		})

		slog.Info("constraint dropped", "schema", schemaName, "table", tableName, "constraint", constraintName)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleAddUniqueConstraint(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		if projectID == "" || tableName == "" {
			jsonError(w, "project ID and table name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var req struct {
			Column string `json:"column"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := AddUniqueConstraint(r.Context(), pool, schemaName, tableName, req.Column); err != nil {
			slog.Error("add unique constraint failed", "error", err, "schema", schemaName, "table", tableName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "add_unique_constraint", tableName, &req.Column, nil)

		slog.Info("unique constraint added", "schema", schemaName, "table", tableName, "column", req.Column)
		jsonResponse(w, map[string]string{"status": "created", "constraint": "uq_" + tableName + "_" + req.Column}, http.StatusCreated)
	}
}

func handleListIndexes(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		if projectID == "" || tableName == "" {
			jsonError(w, "project ID and table name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		indexes, err := GetTableIndexes(r.Context(), pool, schemaName, tableName)
		if err != nil {
			slog.Error("list indexes failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if indexes == nil {
			indexes = []IndexInfo{}
		}

		jsonResponse(w, indexes, http.StatusOK)
	}
}

func handleCreateIndex(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		if projectID == "" || tableName == "" {
			jsonError(w, "project ID and table name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var req struct {
			Column string `json:"column"`
			Unique bool   `json:"unique"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := CreateIndex(r.Context(), pool, schemaName, tableName, req.Column, req.Unique); err != nil {
			slog.Error("create index failed", "error", err, "schema", schemaName, "table", tableName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		colName := req.Column
		logSchemaChange(pool, r, projectID, "create_index", tableName, &colName, map[string]any{
			"unique": req.Unique,
		})

		indexName := "idx_" + tableName + "_" + req.Column
		slog.Info("index created", "schema", schemaName, "table", tableName, "index", indexName)
		jsonResponse(w, map[string]string{"status": "created", "index": indexName}, http.StatusCreated)
	}
}

func handleDropIndex(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")
		indexName := chi.URLParam(r, "index")

		if projectID == "" || tableName == "" || indexName == "" {
			jsonError(w, "project ID, table name, and index name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		if err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		if err := DropIndex(r.Context(), pool, schemaName, indexName); err != nil {
			slog.Error("drop index failed", "error", err, "schema", schemaName, "index", indexName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "drop_index", tableName, nil, map[string]any{
			"index": indexName,
		})

		slog.Info("index dropped", "schema", schemaName, "index", indexName)
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleFunctions returns a chi.Router for schema function operations.
// Mounted at /platform/projects/{id}/schema/functions
func HandleFunctions(pool *pgxpool.Pool) chi.Router {
	r := chi.NewRouter()
	r.Get("/", handleListFunctions(pool))
	r.Post("/", handleCreateFunction(pool))
	r.Delete("/{funcName}", handleDropFunction(pool))
	return r
}

func handleListFunctions(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			jsonError(w, "project ID is required", http.StatusBadRequest)
			return
		}

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		funcs, err := ListFunctions(r.Context(), pool, schemaName)
		if err != nil {
			slog.Error("list functions failed", "error", err, "schema", schemaName)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		jsonResponse(w, funcs, http.StatusOK)
	}
}

func handleCreateFunction(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			jsonError(w, "project ID is required", http.StatusBadRequest)
			return
		}

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var req CreateFunctionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := CreateFunction(r.Context(), pool, schemaName, req); err != nil {
			slog.Error("create function failed", "error", err, "schema", schemaName, "function", req.Name)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "create_function", req.Name, nil, map[string]any{
			"language": req.Language,
			"returns":  req.Returns,
		})

		slog.Info("function created", "schema", schemaName, "function", req.Name)
		jsonResponse(w, map[string]string{
			"status":   "created",
			"function": req.Name,
		}, http.StatusCreated)
	}
}

func handleDropFunction(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		funcName := chi.URLParam(r, "funcName")

		if projectID == "" || funcName == "" {
			jsonError(w, "project ID and function name are required", http.StatusBadRequest)
			return
		}

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		if err := DropFunction(r.Context(), pool, schemaName, funcName); err != nil {
			slog.Error("drop function failed", "error", err, "schema", schemaName, "function", funcName)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "drop_function", funcName, nil, nil)

		slog.Info("function dropped", "schema", schemaName, "function", funcName)
		w.WriteHeader(http.StatusNoContent)
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

// handleToggleRLS enables or disables RLS on a table.
func handleToggleRLS(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		qt := qualifiedTable(schemaName, tableName)
		action := "ENABLE"
		if !body.Enabled {
			action = "DISABLE"
		}

		sql := fmt.Sprintf("ALTER TABLE %s %s ROW LEVEL SECURITY", qt, action)
		if _, err := pool.Exec(r.Context(), sql); err != nil {
			slog.Error("toggle RLS failed", "error", err, "table", tableName, "action", action)
			jsonError(w, "failed to toggle RLS: "+err.Error(), http.StatusInternalServerError)
			return
		}

		logSchemaChange(pool, r, projectID, "toggle_rls", tableName, nil, map[string]any{"enabled": body.Enabled})
		slog.Info("RLS toggled", "schema", schemaName, "table", tableName, "enabled", body.Enabled)

		jsonResponse(w, map[string]any{"status": "ok", "rls_enabled": body.Enabled}, http.StatusOK)
	}
}

func handleListPolicies(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		var schemaName string
		if err := pool.QueryRow(r.Context(), `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		policies, err := ListPolicies(r.Context(), pool, schemaName, tableName)
		if err != nil {
			slog.Error("list policies failed", "error", err)
			jsonError(w, "failed to list policies", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, policies, http.StatusOK)
	}
}

func handleApplyPreset(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		var schemaName string
		if err := pool.QueryRow(r.Context(), `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var body struct {
			Preset       string `json:"preset"`
			UserIDColumn string `json:"user_id_column"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := ApplyPolicyPreset(r.Context(), pool, schemaName, tableName, body.Preset, body.UserIDColumn); err != nil {
			slog.Error("apply preset failed", "error", err)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "apply_rls_preset", tableName, nil, map[string]any{"preset": body.Preset})
		jsonResponse(w, map[string]any{"status": "ok", "preset": body.Preset}, http.StatusOK)
	}
}

func handleCreatePolicy(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")

		var schemaName string
		if err := pool.QueryRow(r.Context(), `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		var body struct {
			Name      string `json:"name"`
			Command   string `json:"command"`
			Using     string `json:"using"`
			WithCheck string `json:"with_check"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if body.Name == "" || body.Command == "" {
			jsonError(w, "name and command are required", http.StatusBadRequest)
			return
		}

		if err := CreateCustomPolicy(r.Context(), pool, schemaName, tableName, body.Name, body.Command, body.Using, body.WithCheck); err != nil {
			slog.Error("create policy failed", "error", err)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		logSchemaChange(pool, r, projectID, "create_policy", tableName, nil, map[string]any{"policy": body.Name, "command": body.Command})
		jsonResponse(w, map[string]any{"status": "ok", "policy": body.Name}, http.StatusCreated)
	}
}

func handleDropPolicy(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		tableName := chi.URLParam(r, "table")
		policyName := chi.URLParam(r, "policy")

		var schemaName string
		if err := pool.QueryRow(r.Context(), `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		if err := DropPolicy(r.Context(), pool, schemaName, tableName, policyName); err != nil {
			slog.Error("drop policy failed", "error", err)
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		logSchemaChange(pool, r, projectID, "drop_policy", tableName, nil, map[string]any{"policy": policyName})
		w.WriteHeader(http.StatusNoContent)
	}
}
