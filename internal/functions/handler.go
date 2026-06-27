package functions

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeGetError maps a Service.Get error to the right HTTP shape. Before
// this, every error was masked as 404 "function not found" — which hid
// real failures (vault decryption errors after the #206 sealed env_vars
// migration, schema lookup failures, transient pool errors) behind a
// lie. The user-visible symptom was: "Function not found" appearing on
// every function in the console even though they all existed in the DB.
//
// Strategy: a true row-miss is a 404 with the original short message;
// anything else surfaces as 500 with the underlying error string so the
// operator can see what's actually wrong (e.g. "decrypt env_vars
// (key_version 1): …"). The original error is also slog'd for ops.
func writeGetError(w http.ResponseWriter, projectID, name string, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Info("get edge function: not found", "project_id", projectID, "name", name)
		jsonError(w, "function not found", http.StatusNotFound)
		return
	}
	slog.Error("get edge function failed", "project_id", projectID, "name", name, "error", err)
	jsonError(w, err.Error(), http.StatusInternalServerError)
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
			writeGetError(w, projectID, name, err)
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

// HandleListTriggers returns triggers for a function.
func HandleListTriggers(svc *Service, trigSvc *TriggerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")

		fn, err := svc.Get(r.Context(), projectID, name)
		if err != nil {
			writeGetError(w, projectID, name, err)
			return
		}

		triggers, err := trigSvc.List(r.Context(), projectID, fn.ID)
		if err != nil {
			slog.Error("list function triggers failed", "error", err)
			jsonError(w, "failed to list triggers", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(triggers)
	}
}

// HandleCreateTrigger creates a new trigger for a function.
func HandleCreateTrigger(svc *Service, trigSvc *TriggerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")

		fn, err := svc.Get(r.Context(), projectID, name)
		if err != nil {
			writeGetError(w, projectID, name, err)
			return
		}

		var req CreateTriggerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		trigger, err := trigSvc.Create(r.Context(), projectID, fn.ID, req)
		if err != nil {
			slog.Error("create function trigger failed", "error", err)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(trigger)
	}
}

// HandleDeleteTrigger removes a trigger from a function.
func HandleDeleteTrigger(trigSvc *TriggerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		triggerID := chi.URLParam(r, "triggerId")

		if err := trigSvc.Delete(r.Context(), projectID, triggerID); err != nil {
			slog.Error("delete function trigger failed", "error", err)
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}
}

// HandleListVersions returns the version history for a function.
func HandleListVersions(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")

		versions, err := svc.ListVersions(r.Context(), projectID, name)
		if err != nil {
			slog.Error("list function versions failed", "error", err)
			jsonError(w, "failed to list versions", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(versions)
	}
}

// HandleRollback restores a function to a previous version.
func HandleRollback(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")

		var req struct {
			Version int `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Version <= 0 {
			jsonError(w, "version must be a positive integer", http.StatusBadRequest)
			return
		}

		fn, err := svc.Rollback(r.Context(), projectID, name, req.Version)
		if err != nil {
			slog.Error("rollback function failed", "error", err)
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fn)
	}
}

// HandleMetrics returns aggregated invocation stats for a function.
func HandleMetrics(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		name := chi.URLParam(r, "name")
		period := r.URL.Query().Get("period")

		metrics, err := svc.GetMetrics(r.Context(), projectID, name, period)
		if err != nil {
			slog.Error("get function metrics failed", "error", err)
			jsonError(w, "failed to get metrics", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metrics)
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
