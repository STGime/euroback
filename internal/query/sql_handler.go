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
