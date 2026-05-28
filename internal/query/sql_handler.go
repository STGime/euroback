package query

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// auditDDLFromSQL inspects the SQL the runner just executed for any DDL
// shapes and writes them to schema_changes. Closes #120: SQL that lands
// outside the typed handlers (console SQL editor, MCP runSQL, SDK
// runSQL) used to silently skip the migration-history audit. This pass
// catches the common shapes (CREATE/DROP TABLE, ADD/DROP/ALTER/RENAME
// COLUMN, RENAME TABLE, CREATE/DROP INDEX) without needing event-
// trigger superuser privileges that managed Postgres won't grant.
//
// Best-effort: detection misses (direct DB shell, weird syntax) and
// audit-write failures both stay silent in the user request path. The
// existing typed-handler logSchemaChange in ddl_handler.go covers the
// authoritative path for handler-driven DDL — this is the SQL-runner
// supplement.
func auditDDLFromSQL(r *http.Request, engine *QueryEngine, sql string) {
	projectID := ProjectIDFromContext(r.Context())
	if projectID == "" {
		// Without a project the schema_changes FK would reject the
		// insert. SDK paths set this via the apikey middleware; the
		// platform path via PlatformTenantContext. Both run before
		// these handlers — if we ever land here without it, log so
		// the gap is visible.
		slog.Debug("auditDDLFromSQL: no project_id in context, skipping")
		return
	}
	ops := DetectDDL(sql)
	for _, op := range ops {
		logSchemaChange(engine.pool, r, projectID, op.Action, op.TableName, op.ColumnName, op.Detail)
	}
}

// SQLRequest is the JSON body for POST /v1/db/sql.
type SQLRequest struct {
	SQL   string `json:"sql"`
	Limit int    `json:"limit,omitempty"`
	// ReadOnly opts the call into a read-only transaction even on the
	// platform endpoint. The MCP server sets this to true by default
	// (closes #165 / part of #164 mitigation) so a prompt-injected
	// query cannot mutate state. Writes raise SQLSTATE 25006 and roll
	// back. Honoured only when the handler-level forceReadOnly is
	// false; on the SDK path forceReadOnly already gates writes via
	// ValidateSelectOnly.
	ReadOnly bool `json:"read_only,omitempty"`
}

// SQLResponse is the JSON response for a successful SQL execution.
type SQLResponse struct {
	Columns         []string                 `json:"columns"`
	Rows            []map[string]interface{} `json:"rows"`
	RowCount        int                      `json:"row_count"`
	ExecutionTimeMs float64                  `json:"execution_time_ms"`
}

// HandleSQL returns an http.HandlerFunc for POST /v1/db/sql (SDK — read-only).
func HandleSQL(engine *QueryEngine) http.HandlerFunc {
	return handleSQLInternal(engine, true)
}

// HandlePlatformSQL returns an http.HandlerFunc for POST /platform/.../data/sql (console — full access).
func HandlePlatformSQL(engine *QueryEngine) http.HandlerFunc {
	return handleSQLInternal(engine, false)
}

// SQLTransactionRequest is the JSON body for the multi-statement endpoint.
// Each element of Statements is executed as its own statement inside a
// single transaction. Mirrors Neon's run_sql_transaction shape.
type SQLTransactionRequest struct {
	Statements []string `json:"statements"`
	Limit      int      `json:"limit,omitempty"`
	// ReadOnly wraps the whole transaction in SET TRANSACTION READ ONLY.
	// MCP-origin requests set this true to block prompt-injected writes
	// (closes part of #165).
	ReadOnly bool `json:"read_only,omitempty"`
}

// SQLTransactionResponse is the JSON response for HandlePlatformSQLTransaction.
type SQLTransactionResponse struct {
	Statements      []StatementResult `json:"statements"`
	StatementCount  int               `json:"statement_count"`
	ExecutionTimeMs float64           `json:"execution_time_ms"`
}

// HandlePlatformSQLTransaction returns an http.HandlerFunc for
// POST /platform/.../data/sql/transaction. The body is
// {"statements": ["...", "..."]} and each statement runs in its own
// pgx Exec/Query call, all inside one transaction. If any statement
// fails the whole transaction is rolled back.
func HandlePlatformSQLTransaction(engine *QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		var req SQLTransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.Statements) == 0 {
			jsonError(w, "statements must be a non-empty array", http.StatusBadRequest)
			return
		}

		start := time.Now()
		results, err := engine.ExecuteSQLTransaction(r.Context(), schema, req.Statements, req.Limit, req.ReadOnly)
		elapsed := time.Since(start)
		if err != nil {
			slog.Error("sql transaction failed", "error", err, "schema", schema, "completed", len(results), "total", len(req.Statements))
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Closes #120 for the multi-statement path. Each statement
		// committed successfully (the whole tx did), so audit any DDL
		// shapes we can recognise.
		for _, stmt := range req.Statements {
			auditDDLFromSQL(r, engine, stmt)
		}

		jsonResponse(w, SQLTransactionResponse{
			Statements:      results,
			StatementCount:  len(results),
			ExecutionTimeMs: float64(elapsed.Microseconds()) / 1000.0,
		}, http.StatusOK)
	}
}

func handleSQLInternal(engine *QueryEngine, forceReadOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		var req SQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Effective read-only: the SDK path (forceReadOnly=true) is
		// always read-only; the platform path (forceReadOnly=false)
		// honours the body's `read_only` field. Closes part of #165:
		// the MCP server sets read_only=true on its outbound calls so
		// a prompt-injected query cannot mutate state.
		readOnly := forceReadOnly || req.ReadOnly

		if forceReadOnly {
			// SDK: validate SELECT-only.
			if err := ValidateSelectOnly(req.SQL); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			// Closes advisory GHSA-5cj5-c9f7-9gcj — without this check the
			// gateway pool's broad cross-schema grants combined with the
			// service-role RLS bypass let any caller with a secret API key
			// read another tenant's data via fully-qualified `tenant_<other>.…`
			// references. The check rejects qualified references to any
			// schema other than the caller's tenant schema (and pg_temp).
			if err := ValidateNoCrossSchemaRefs(req.SQL, schema); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		// Guard: pgx Tx.Exec uses the extended query protocol, which runs
		// only the first statement in a multi-statement string and silently
		// drops the rest. Reject such input here so callers cannot mistake
		// a partial run for success.
		if HasMultipleStatements(req.SQL) {
			jsonError(w,
				"input contains multiple SQL statements; this endpoint accepts a single statement. "+
					"Use POST /platform/projects/{id}/data/sql/transaction with a JSON body "+
					`{"statements": ["...", "..."]}`+" to run a multi-statement migration in one transaction.",
				http.StatusBadRequest)
			return
		}

		// Cap limit.
		maxRows := req.Limit
		if maxRows <= 0 || maxRows > 1000 {
			maxRows = 1000
		}

		start := time.Now()
		columns, rows, err := engine.ExecuteSQL(r.Context(), schema, req.SQL, maxRows, readOnly)
		elapsed := time.Since(start)

		if err != nil {
			slog.Error("sql execution failed", "error", err, "schema", schema)
			// Closes #52. SDK callers (readOnly path) get a sanitised
			// message — the raw pgx Error() output leaks Detail/Hint/
			// position which can echo offending values, internal paths,
			// or schema layout. Platform admins running ad-hoc SQL
			// through the console keep the full message because they
			// already see the schema.
			if readOnly {
				jsonError(w, sanitizeSDKSQLError(err), http.StatusBadRequest)
			} else {
				jsonError(w, err.Error(), http.StatusBadRequest)
			}
			return
		}

		if rows == nil {
			rows = make([]map[string]interface{}, 0)
		}

		// Closes #120 for the single-statement path. SDK (read-only)
		// can't issue DDL via this endpoint — ValidateSelectOnly above
		// rejects it — so we only need to audit when the caller had
		// write access.
		if !readOnly {
			auditDDLFromSQL(r, engine, req.SQL)
		}

		resp := SQLResponse{
			Columns:         columns,
			Rows:            rows,
			RowCount:        len(rows),
			ExecutionTimeMs: float64(elapsed.Microseconds()) / 1000.0,
		}

		slog.Debug("sql query complete", "schema", schema, "rows", len(rows), "ms", resp.ExecutionTimeMs)
		jsonResponse(w, resp, http.StatusOK)
	}
}
