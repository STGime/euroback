package plans

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// usageResponse combines current usage with plan limits for a project.
type usageResponse struct {
	Usage  *ProjectUsage `json:"usage"`
	Limits *PlanLimits   `json:"limits"`
}

// HandleGetUsage returns an HTTP handler that responds with the project's
// current resource usage and plan limits.
func HandleGetUsage(svc *LimitsService, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			jsonError(w, "missing project id", http.StatusBadRequest)
			return
		}

		// Look up the project's schema name.
		var schemaName string
		err := pool.QueryRow(r.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName)
		if err != nil {
			slog.Error("get usage: project lookup failed", "project_id", projectID, "error", err)
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		usage, err := svc.GetUsage(r.Context(), projectID, schemaName)
		if err != nil {
			slog.Error("get usage: failed", "project_id", projectID, "error", err)
			jsonError(w, "failed to retrieve usage", http.StatusInternalServerError)
			return
		}

		limits, err := svc.GetProjectLimits(r.Context(), projectID)
		if err != nil {
			slog.Error("get usage: limits lookup failed", "project_id", projectID, "error", err)
			jsonError(w, "failed to retrieve plan limits", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(usageResponse{
			Usage:  usage,
			Limits: limits,
		})
	}
}

// HandleGetPlans returns an HTTP handler that responds with all available
// plan limits as a JSON array.
func HandleGetPlans(svc *LimitsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := svc.pool.Query(r.Context(),
			`SELECT plan, db_size_mb, storage_mb, bandwidth_mb, mau_limit,
			        rate_limit_rps, ws_connections, upload_size_mb, webhook_limit,
			        project_limit, log_retention_days, custom_templates
			 FROM plan_limits ORDER BY plan`)
		if err != nil {
			slog.Error("get plans: query failed", "error", err)
			jsonError(w, "failed to retrieve plans", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		plans := make([]PlanLimits, 0)
		for rows.Next() {
			var l PlanLimits
			if err := rows.Scan(
				&l.Plan, &l.DBSizeMB, &l.StorageMB, &l.BandwidthMB, &l.MAULimit,
				&l.RateLimitRPS, &l.WSConnections, &l.UploadSizeMB, &l.WebhookLimit,
				&l.ProjectLimit, &l.LogRetentionDays, &l.CustomTemplates,
			); err != nil {
				slog.Error("get plans: scan failed", "error", err)
				jsonError(w, "internal server error", http.StatusInternalServerError)
				return
			}
			plans = append(plans, l)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(plans)
	}
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
