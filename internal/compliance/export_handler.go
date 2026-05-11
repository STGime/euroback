package compliance

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func writeExportJSON(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeExportError(w http.ResponseWriter, msg string, status int) {
	writeExportJSON(w, map[string]string{"error": msg}, status)
}

// HandleRequestTenantExport creates a full-tenant DSAR export job.
// POST /platform/projects/{id}/compliance/export
func HandleRequestTenantExport(exportSvc *ExportService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeExportError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			Format string `json:"format"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		if body.Format == "" {
			body.Format = "json"
		}
		if body.Format != "json" && body.Format != "csv" {
			writeExportError(w, "format must be 'json' or 'csv'", http.StatusBadRequest)
			return
		}

		limited, err := exportSvc.CheckRateLimit(r.Context(), projectID, nil)
		if err != nil {
			slog.Error("export rate limit check failed", "error", err)
			writeExportError(w, "internal error", http.StatusInternalServerError)
			return
		}
		if limited {
			writeExportError(w, "rate limit exceeded: 1 tenant export per hour", http.StatusTooManyRequests)
			return
		}

		req, err := exportSvc.CreateExportRequest(r.Context(), projectID, nil, body.Format, claims.Subject, "platform")
		if err != nil {
			slog.Error("create export request failed", "error", err)
			writeExportError(w, "failed to create export request", http.StatusInternalServerError)
			return
		}

		if err := exportSvc.EnqueueTenantExport(r.Context(), req.ID, projectID, body.Format); err != nil {
			slog.Error("enqueue tenant export job failed", "error", err)
			_ = exportSvc.MarkFailed(r.Context(), req.ID, "failed to enqueue job")
			writeExportError(w, "failed to enqueue export", http.StatusInternalServerError)
			return
		}

		writeExportJSON(w, req, http.StatusAccepted)
	}
}

// HandleRequestUserExport creates a per-user DSAR export job.
// POST /platform/projects/{id}/compliance/user-export
func HandleRequestUserExport(exportSvc *ExportService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeExportError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			UserID string `json:"user_id"`
			Format string `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserID == "" {
			writeExportError(w, "user_id is required", http.StatusBadRequest)
			return
		}
		if body.Format == "" {
			body.Format = "json"
		}
		if body.Format != "json" && body.Format != "csv" {
			writeExportError(w, "format must be 'json' or 'csv'", http.StatusBadRequest)
			return
		}

		limited, err := exportSvc.CheckRateLimit(r.Context(), projectID, &body.UserID)
		if err != nil {
			writeExportError(w, "internal error", http.StatusInternalServerError)
			return
		}
		if limited {
			writeExportError(w, "rate limit exceeded: 1 user export per 24 hours", http.StatusTooManyRequests)
			return
		}

		req, err := exportSvc.CreateExportRequest(r.Context(), projectID, &body.UserID, body.Format, claims.Subject, "platform")
		if err != nil {
			writeExportError(w, "failed to create export request", http.StatusInternalServerError)
			return
		}

		if err := exportSvc.EnqueueUserExport(r.Context(), req.ID, projectID, body.UserID, body.Format); err != nil {
			_ = exportSvc.MarkFailed(r.Context(), req.ID, "failed to enqueue job")
			writeExportError(w, "failed to enqueue export", http.StatusInternalServerError)
			return
		}

		writeExportJSON(w, req, http.StatusAccepted)
	}
}

// HandleListExports returns paginated export history for a project.
// GET /platform/projects/{id}/compliance/exports
func HandleListExports(exportSvc *ExportService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

		exports, err := exportSvc.ListExports(r.Context(), projectID, limit, offset)
		if err != nil {
			writeExportError(w, "failed to list exports", http.StatusInternalServerError)
			return
		}
		if exports == nil {
			exports = []ExportRequest{}
		}
		writeExportJSON(w, map[string]interface{}{"exports": exports}, http.StatusOK)
	}
}

// HandleGetExport returns a single export request with download URL if completed.
// GET /platform/projects/{id}/compliance/exports/{exportId}
func HandleGetExport(exportSvc *ExportService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		exportID := chi.URLParam(r, "exportId")

		req, err := exportSvc.GetExportRequest(r.Context(), exportID, projectID)
		if err != nil {
			writeExportError(w, "export not found", http.StatusNotFound)
			return
		}

		if req.Status == "completed" && req.S3Key != nil {
			var bucket string
			_ = exportSvc.Pool.QueryRow(r.Context(), `SELECT s3_bucket FROM projects WHERE id = $1`, projectID).Scan(&bucket)
			if bucket != "" {
				url, err := exportSvc.GenerateDownloadURL(r.Context(), bucket, *req.S3Key)
				if err == nil {
					req.DownloadURL = url
				}
			}
		}

		writeExportJSON(w, req, http.StatusOK)
	}
}

// HandleSelfServeExport allows an end-user to export their own data.
// POST /v1/auth/me/export
func HandleSelfServeExport(pool *pgxpool.Pool, s3 *storage.S3Client, auditSvc *audit.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			writeExportError(w, "missing project context", http.StatusUnauthorized)
			return
		}

		endUserClaims, _ := auth.EndUserClaimsFromContext(r.Context())
		userID := ""
		if endUserClaims != nil {
			userID = endUserClaims.UserID
		}
		if userID == "" {
			writeExportError(w, "authentication required", http.StatusUnauthorized)
			return
		}

		var body struct {
			Format string `json:"format"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		if body.Format == "" {
			body.Format = "json"
		}
		if body.Format != "json" && body.Format != "csv" {
			writeExportError(w, "format must be 'json' or 'csv'", http.StatusBadRequest)
			return
		}

		exportSvc := NewExportService(pool, s3, auditSvc)

		limited, err := exportSvc.CheckRateLimit(r.Context(), pc.ProjectID, &userID)
		if err != nil {
			writeExportError(w, "internal error", http.StatusInternalServerError)
			return
		}
		if limited {
			writeExportError(w, "rate limit exceeded: 1 export per 24 hours", http.StatusTooManyRequests)
			return
		}

		req, err := exportSvc.CreateExportRequest(r.Context(), pc.ProjectID, &userID, body.Format, userID, "enduser")
		if err != nil {
			writeExportError(w, "failed to create export request", http.StatusInternalServerError)
			return
		}

		if err := exportSvc.EnqueueUserExport(r.Context(), req.ID, pc.ProjectID, userID, body.Format); err != nil {
			_ = exportSvc.MarkFailed(r.Context(), req.ID, "failed to enqueue job")
			writeExportError(w, "failed to enqueue export", http.StatusInternalServerError)
			return
		}

		writeExportJSON(w, req, http.StatusAccepted)
	}
}

// HandleSelfServeExportStatus returns the status of an end-user's own export.
// GET /v1/auth/me/export/{exportId}
func HandleSelfServeExportStatus(pool *pgxpool.Pool, s3 *storage.S3Client, auditSvc *audit.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			writeExportError(w, "missing project context", http.StatusUnauthorized)
			return
		}

		endUserClaims, _ := auth.EndUserClaimsFromContext(r.Context())
		userID := ""
		if endUserClaims != nil {
			userID = endUserClaims.UserID
		}
		if userID == "" {
			writeExportError(w, "authentication required", http.StatusUnauthorized)
			return
		}

		exportID := chi.URLParam(r, "exportId")
		exportSvc := NewExportService(pool, s3, auditSvc)
		req, err := exportSvc.GetExportRequestForUser(r.Context(), exportID, pc.ProjectID, userID)
		if err != nil {
			writeExportError(w, "export not found", http.StatusNotFound)
			return
		}

		if req.Status == "completed" && req.S3Key != nil {
			var bucket string
			_ = pool.QueryRow(r.Context(), `SELECT s3_bucket FROM projects WHERE id = $1`, pc.ProjectID).Scan(&bucket)
			if bucket != "" {
				url, err := exportSvc.GenerateDownloadURL(r.Context(), bucket, *req.S3Key)
				if err == nil {
					req.DownloadURL = url
				}
			}
		}

		writeExportJSON(w, req, http.StatusOK)
	}
}
