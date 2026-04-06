package functions

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
)

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// HandleList returns all edge functions for a project.
func HandleList(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		fns, err := svc.List(r.Context(), projectID)
		if err != nil {
			slog.Error("list edge functions failed", "project_id", projectID, "error", err)
			jsonError(w, "failed to list functions", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fns)
	}
}

// HandleGet returns a single edge function with its code.
func HandleGet(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")

		fn, err := svc.Get(r.Context(), projectID, name)
		if err != nil {
			slog.Error("get edge function failed", "project_id", projectID, "name", name, "error", err)
			jsonError(w, "function not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fn)
	}
}

// HandleCreate creates a new edge function with plan limit enforcement.
func HandleCreate(svc *Service, limitsSvc *plans.LimitsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		var req CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Enforce plan limits.
		if limitsSvc != nil {
			limits, err := limitsSvc.GetProjectLimits(r.Context(), projectID)
			if err == nil && limits.EdgeFunctionLimit > 0 {
				count, countErr := svc.Count(r.Context(), projectID)
				if countErr == nil && count >= limits.EdgeFunctionLimit {
					jsonError(w, limits.Plan+" plan limited to "+strconv.Itoa(limits.EdgeFunctionLimit)+" edge functions", http.StatusForbidden)
					return
				}
			}
		}

		fn, err := svc.Create(r.Context(), projectID, req)
		if err != nil {
			slog.Error("create edge function failed", "project_id", projectID, "error", err)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(fn)
	}
}

// HandleUpdate updates an existing edge function.
func HandleUpdate(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")

		var req UpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		fn, err := svc.Update(r.Context(), projectID, name, req)
		if err != nil {
			slog.Error("update edge function failed", "project_id", projectID, "name", name, "error", err)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fn)
	}
}

// HandleDelete deletes an edge function.
func HandleDelete(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")

		if err := svc.Delete(r.Context(), projectID, name); err != nil {
			slog.Error("delete edge function failed", "project_id", projectID, "name", name, "error", err)
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}
}

// HandleLogs returns execution logs for a function.
func HandleLogs(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")

		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			if parsed, err := strconv.Atoi(l); err == nil {
				limit = parsed
			}
		}

		logs, err := svc.GetLogs(r.Context(), projectID, name, limit)
		if err != nil {
			slog.Error("get function logs failed", "project_id", projectID, "name", name, "error", err)
			jsonError(w, "failed to get logs", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(logs)
	}
}
