package cron

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

const (
	// freePlanCronLimit is the max number of cron jobs on the free plan.
	freePlanCronLimit = 2
)

// Routes returns a chi.Router for cron job CRUD operations.
// Mounted at /platform/projects/{id}/cron
func Routes(svc *CronService) chi.Router {
	r := chi.NewRouter()
	r.Get("/", handleList(svc))
	r.Post("/", handleCreate(svc))
	r.Patch("/{jobId}", handleUpdate(svc))
	r.Delete("/{jobId}", handleDelete(svc))
	return r
}

func handleList(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		jobs, err := svc.List(r.Context(), projectID)
		if err != nil {
			slog.Error("list cron jobs failed", "error", err, "project_id", projectID)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		jsonResponse(w, jobs, http.StatusOK)
	}
}

func handleCreate(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		// Parse and validate request body first.
		var req CreateCronJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Validate fields before checking plan limits.
		if err := req.Validate(); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Enforce plan limit: free plan gets 2 cron jobs.
		count, err := svc.Count(r.Context(), projectID)
		if err != nil {
			slog.Error("count cron jobs failed", "error", err, "project_id", projectID)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		var plan string
		err = svc.pool.QueryRow(r.Context(),
			`SELECT COALESCE(plan, 'free') FROM projects WHERE id = $1`, projectID).Scan(&plan)
		if err != nil {
			slog.Error("get project plan failed", "error", err, "project_id", projectID)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if plan == "free" && count >= freePlanCronLimit {
			jsonError(w, "free plan is limited to 2 scheduled jobs — upgrade to pro for unlimited", http.StatusForbidden)
			return
		}

		job, err := svc.Create(r.Context(), projectID, req)
		if err != nil {
			slog.Error("create cron job failed", "error", err, "project_id", projectID)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		jsonResponse(w, job, http.StatusCreated)
	}
}

func handleUpdate(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		jobID := chi.URLParam(r, "jobId")

		var req UpdateCronJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		job, err := svc.Update(r.Context(), projectID, jobID, req)
		if err != nil {
			if err.Error() == "cron job not found" {
				jsonError(w, "cron job not found", http.StatusNotFound)
				return
			}
			slog.Error("update cron job failed", "error", err, "project_id", projectID, "job_id", jobID)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		jsonResponse(w, job, http.StatusOK)
	}
}

func handleDelete(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		jobID := chi.URLParam(r, "jobId")

		err := svc.Delete(r.Context(), projectID, jobID)
		if err != nil {
			if err.Error() == "cron job not found" {
				jsonError(w, "cron job not found", http.StatusNotFound)
				return
			}
			slog.Error("delete cron job failed", "error", err, "project_id", projectID, "job_id", jobID)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func jsonResponse(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
