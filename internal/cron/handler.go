package cron

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
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
	r.Get("/{jobId}/runs", handleListRuns(svc))
	return r
}

// SDKRoutes returns a chi.Router exposing the schedule surface to the JS
// SDK (`eb.functions.schedules.*`). Mounted at /v1/schedules under the
// API-key middleware. Schedules are addressed by their stable name, not
// by server-allocated UUID — see #112.
//
// Service-key gating is done by the route wrapper (see router.go), since
// editing schedules is a control-plane operation and public keys must
// not have write access.
func SDKRoutes(svc *CronService) chi.Router {
	r := chi.NewRouter()
	r.Get("/", handleSDKList(svc))
	r.Post("/", handleSDKCreate(svc))
	r.Get("/{name}", handleSDKGet(svc))
	r.Patch("/{name}", handleSDKUpdate(svc))
	r.Delete("/{name}", handleSDKDelete(svc))
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

		var req CreateCronJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := req.Validate(); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := enforcePlanLimit(r, svc, projectID); err != nil {
			jsonError(w, err.message, err.status)
			return
		}

		job, err := svc.Create(r.Context(), projectID, req)
		if err != nil {
			if errors.Is(err, ErrNameAlreadyExists) {
				jsonError(w, err.Error(), http.StatusConflict)
				return
			}
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
			if errors.Is(err, ErrNotFound) {
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
			if errors.Is(err, ErrNotFound) {
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

func handleListRuns(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := chi.URLParam(r, "jobId")
		if jobID == "" {
			jsonError(w, "job ID is required", http.StatusBadRequest)
			return
		}

		runs, err := svc.ListRuns(r.Context(), jobID, 20)
		if err != nil {
			slog.Error("list cron job runs failed", "error", err, "job_id", jobID)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		jsonResponse(w, runs, http.StatusOK)
	}
}

// ── SDK handlers (addressed by name, project from API-key context) ──

func projectIDFromAPIKey(r *http.Request) (string, bool) {
	pc, ok := auth.ProjectFromContext(r.Context())
	if !ok || pc == nil {
		return "", false
	}
	return pc.ProjectID, true
}

func handleSDKList(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, ok := projectIDFromAPIKey(r)
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}
		jobs, err := svc.List(r.Context(), projectID)
		if err != nil {
			slog.Error("sdk list schedules failed", "error", err, "project_id", projectID)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, jobs, http.StatusOK)
	}
}

func handleSDKGet(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, ok := projectIDFromAPIKey(r)
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}
		name := chi.URLParam(r, "name")
		job, err := svc.GetByName(r.Context(), projectID, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				jsonError(w, "schedule not found", http.StatusNotFound)
				return
			}
			slog.Error("sdk get schedule failed", "error", err, "project_id", projectID, "name", name)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, job, http.StatusOK)
	}
}

func handleSDKCreate(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, ok := projectIDFromAPIKey(r)
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}

		var req CreateCronJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := req.Validate(); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := enforcePlanLimit(r, svc, projectID); err != nil {
			jsonError(w, err.message, err.status)
			return
		}

		job, err := svc.Create(r.Context(), projectID, req)
		if err != nil {
			if errors.Is(err, ErrNameAlreadyExists) {
				// 409 lets the SDK surface this as `already_exists`
				// per #112 — callers should retry with update().
				jsonError(w, err.Error(), http.StatusConflict)
				return
			}
			slog.Error("sdk create schedule failed", "error", err, "project_id", projectID)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResponse(w, job, http.StatusCreated)
	}
}

func handleSDKUpdate(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, ok := projectIDFromAPIKey(r)
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}
		name := chi.URLParam(r, "name")

		var req UpdateCronJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		job, err := svc.UpdateByName(r.Context(), projectID, name, req)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				jsonError(w, "schedule not found", http.StatusNotFound)
				return
			}
			slog.Error("sdk update schedule failed", "error", err, "project_id", projectID, "name", name)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResponse(w, job, http.StatusOK)
	}
}

func handleSDKDelete(svc *CronService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, ok := projectIDFromAPIKey(r)
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}
		name := chi.URLParam(r, "name")
		if err := svc.DeleteByName(r.Context(), projectID, name); err != nil {
			if errors.Is(err, ErrNotFound) {
				jsonError(w, "schedule not found", http.StatusNotFound)
				return
			}
			slog.Error("sdk delete schedule failed", "error", err, "project_id", projectID, "name", name)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// httpErr is a small bag for plan-limit failures so create handlers can
// surface either 403 (over limit) or 500 (DB error) with a single check.
type httpErr struct {
	status  int
	message string
}

func enforcePlanLimit(r *http.Request, svc *CronService, projectID string) *httpErr {
	count, err := svc.Count(r.Context(), projectID)
	if err != nil {
		slog.Error("count cron jobs failed", "error", err, "project_id", projectID)
		return &httpErr{status: http.StatusInternalServerError, message: "internal server error"}
	}
	var plan string
	if err := svc.pool.QueryRow(r.Context(),
		`SELECT COALESCE(plan, 'free') FROM projects WHERE id = $1`, projectID).Scan(&plan); err != nil {
		slog.Error("get project plan failed", "error", err, "project_id", projectID)
		return &httpErr{status: http.StatusInternalServerError, message: "internal server error"}
	}
	if plan == "free" && count >= freePlanCronLimit {
		return &httpErr{status: http.StatusForbidden, message: "free plan is limited to 2 scheduled jobs — upgrade to pro for unlimited"}
	}
	return nil
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
