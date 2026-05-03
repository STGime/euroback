package query

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// SQLRequest is the JSON body for POST /v1/db/sql.
type SQLRequest struct {
	SQL   string `json:"sql"`
	Limit int    `json:"limit,omitempty"`
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
		results, err := engine.ExecuteSQLTransaction(r.Context(), schema, req.Statements, req.Limit)
		elapsed := time.Since(start)
		if err != nil {
			slog.Error("sql transaction failed", "error", err, "schema", schema, "completed", len(results), "total", len(req.Statements))
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		jsonResponse(w, SQLTransactionResponse{
			Statements:      results,
			StatementCount:  len(results),
			ExecutionTimeMs: float64(elapsed.Microseconds()) / 1000.0,
		}, http.StatusOK)
	}
}

func handleSQLInternal(engine *QueryEngine, readOnly bool) http.HandlerFunc {
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

		if readOnly {
			// SDK: validate SELECT-only.
			if err := ValidateSelectOnly(req.SQL); err != nil {
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
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		if rows == nil {
			rows = make([]map[string]interface{}, 0)
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
