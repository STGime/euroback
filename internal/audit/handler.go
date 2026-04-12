package audit

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// HandleList returns audit log entries for a project.
// GET /platform/projects/{id}/compliance/audit-log?limit=50&offset=0&action=auth_config.updated
func HandleList(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"missing project id"}`, http.StatusBadRequest)
			return
		}

		params := ListParams{Limit: 50}
		if l := r.URL.Query().Get("limit"); l != "" {
			if v, err := strconv.Atoi(l); err == nil {
				params.Limit = v
			}
		}
		if o := r.URL.Query().Get("offset"); o != "" {
			if v, err := strconv.Atoi(o); err == nil {
				params.Offset = v
			}
		}
		params.Action = r.URL.Query().Get("action")

		result, err := svc.List(r.Context(), projectID, params)
		if err != nil {
			http.Error(w, `{"error":"failed to query audit log"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}
