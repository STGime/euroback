package compliance

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// HandleDPAReport returns an HTTP handler that generates the full DPA compliance
// report for a project.
// GET /platform/projects/{id}/compliance/dpa-report
func HandleDPAReport(svc *ComplianceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"missing project id"}`, http.StatusBadRequest)
			return
		}

		report, err := svc.GenerateReport(r.Context(), projectID)
		if err != nil {
			slog.Error("generating DPA report", "project_id", projectID, "error", err)
			http.Error(w, `{"error":"failed to generate report"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(report)
	}
}

// HandlePublicSubProcessors returns an HTTP handler that lists every
// active sub-processor Eurobase has authorised, independent of any
// project. This is the platform-wide list the DPA (Section 7 and
// Annex 3), the Privacy Policy and the Terms cite as
// "/legal/sub-processors"; the console page at that path renders it.
// Anonymous by design: a prospect or a customer's DPO must be able to
// read it without an account. Read-only, no user input, and cached
// for five minutes so it cannot be used to hammer the registry table.
// GET /platform/public/sub-processors
func HandlePublicSubProcessors(svc *ComplianceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		processors, err := svc.ListAllSubProcessors(r.Context())
		if err != nil {
			slog.Error("listing public sub-processors", "error", err)
			http.Error(w, `{"error":"failed to list sub-processors"}`, http.StatusInternalServerError)
			return
		}
		if processors == nil {
			processors = []SubProcessor{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		json.NewEncoder(w).Encode(processors)
	}
}

// HandleSubProcessors returns an HTTP handler that lists the active sub-processors
// for a project.
// GET /platform/projects/{id}/compliance/sub-processors
func HandleSubProcessors(svc *ComplianceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if projectID == "" {
			http.Error(w, `{"error":"missing project id"}`, http.StatusBadRequest)
			return
		}

		processors, err := svc.GetActiveSubProcessors(r.Context(), projectID)
		if err != nil {
			slog.Error("listing sub-processors", "project_id", projectID, "error", err)
			http.Error(w, `{"error":"failed to list sub-processors"}`, http.StatusInternalServerError)
			return
		}

		if processors == nil {
			processors = []SubProcessor{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(processors)
	}
}
