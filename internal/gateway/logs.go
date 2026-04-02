package gateway

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequestLog is a single log entry returned by the API.
type RequestLog struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int       `json:"latency_ms"`
	IPAddress  string    `json:"ip_address"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
}

// LogStats contains aggregate statistics for request logs.
type LogStats struct {
	TotalRequests int     `json:"total_requests"`
	ErrorCount    int     `json:"error_count"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	P95LatencyMs  float64 `json:"p95_latency_ms"`
}

// LogsResponse is the response body for GET /platform/projects/{id}/logs.
type LogsResponse struct {
	Logs  []RequestLog `json:"logs"`
	Total int          `json:"total"`
	Stats LogStats     `json:"stats"`
}

// HandleLogs returns a handler for listing and filtering request logs.
func HandleLogs(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			writeJSON(w, map[string]string{"error": "project ID is required"}, http.StatusBadRequest)
			return
		}

		q := r.URL.Query()

		limit := 50
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		offset := 0
		if v := q.Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		// Build WHERE clause.
		conditions := []string{"project_id = $1"}
		args := []any{projectID}
		argIdx := 2

		if v := q.Get("method"); v != "" {
			conditions = append(conditions, fmt.Sprintf("method = $%d", argIdx))
			args = append(args, strings.ToUpper(v))
			argIdx++
		}
		if v := q.Get("status_min"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				conditions = append(conditions, fmt.Sprintf("status_code >= $%d", argIdx))
				args = append(args, n)
				argIdx++
			}
		}
		if v := q.Get("status_max"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				conditions = append(conditions, fmt.Sprintf("status_code <= $%d", argIdx))
				args = append(args, n)
				argIdx++
			}
		}
		if v := q.Get("path"); v != "" {
			conditions = append(conditions, fmt.Sprintf("path ILIKE $%d", argIdx))
			args = append(args, "%"+v+"%")
			argIdx++
		}
		if v := q.Get("from"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
				args = append(args, t)
				argIdx++
			}
		}
		if v := q.Get("to"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
				args = append(args, t)
				argIdx++
			}
		}

		// Enforce plan-based log retention.
		var retentionDays int
		err := pool.QueryRow(r.Context(),
			`SELECT COALESCE(pl.log_retention_days, 1)
			 FROM projects p
			 LEFT JOIN plan_limits pl ON pl.plan = COALESCE(p.plan, 'free')
			 WHERE p.id = $1`, projectID,
		).Scan(&retentionDays)
		if err == nil && retentionDays > 0 {
			conditions = append(conditions, fmt.Sprintf("created_at >= now() - interval '%d days'", retentionDays))
		}

		where := strings.Join(conditions, " AND ")

		// Get total count.
		var total int
		countSQL := fmt.Sprintf("SELECT COUNT(*) FROM request_logs WHERE %s", where)
		if err := pool.QueryRow(r.Context(), countSQL, args...).Scan(&total); err != nil {
			slog.Error("count request logs failed", "error", err)
			writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
			return
		}

		// Get stats.
		stats := LogStats{TotalRequests: total}
		statsSQL := fmt.Sprintf(`SELECT
			COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(latency_ms), 0),
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)
			FROM request_logs WHERE %s`, where)
		if err := pool.QueryRow(r.Context(), statsSQL, args...).Scan(
			&stats.ErrorCount, &stats.AvgLatencyMs, &stats.P95LatencyMs,
		); err != nil {
			slog.Error("stats query failed", "error", err)
			// Non-fatal — continue with zero stats.
		}

		// Get paginated logs.
		logsSQL := fmt.Sprintf(
			`SELECT id, project_id, method, path, status_code, latency_ms, COALESCE(ip_address, ''), COALESCE(user_agent, ''), created_at
			 FROM request_logs WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
			where, argIdx, argIdx+1,
		)
		logsArgs := append(args, limit, offset)

		rows, err := pool.Query(r.Context(), logsSQL, logsArgs...)
		if err != nil {
			slog.Error("query request logs failed", "error", err)
			writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		logs := make([]RequestLog, 0)
		for rows.Next() {
			var l RequestLog
			if err := rows.Scan(&l.ID, &l.ProjectID, &l.Method, &l.Path, &l.StatusCode, &l.LatencyMs, &l.IPAddress, &l.UserAgent, &l.CreatedAt); err != nil {
				slog.Error("scan request log failed", "error", err)
				writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
				return
			}
			logs = append(logs, l)
		}

		writeJSON(w, LogsResponse{
			Logs:  logs,
			Total: total,
			Stats: stats,
		}, http.StatusOK)
	}
}

func writeJSON(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
