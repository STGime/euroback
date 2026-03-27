package email

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TemplateHandler handles email template CRUD operations.
type TemplateHandler struct {
	pool      *pgxpool.Pool
	service   *EmailService
	limitsSvc *plans.LimitsService
}

// NewTemplateHandler creates a new template handler.
func NewTemplateHandler(pool *pgxpool.Pool, service *EmailService, limitsSvc *plans.LimitsService) *TemplateHandler {
	return &TemplateHandler{pool: pool, service: service, limitsSvc: limitsSvc}
}

type templateResponse struct {
	TemplateType string `json:"template_type"`
	Subject      string `json:"subject"`
	BodyHTML     string `json:"body_html"`
	IsCustom     bool   `json:"is_custom"`
}

type templateUpdateRequest struct {
	Subject  string `json:"subject"`
	BodyHTML string `json:"body_html"`
}

type previewRequest struct {
	Subject  string `json:"subject"`
	BodyHTML string `json:"body_html"`
}

// HandleList returns all templates for a project (merged with defaults).
func (h *TemplateHandler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")

		rows, err := h.pool.Query(r.Context(),
			`SELECT template_type, subject, body_html FROM public.email_templates WHERE project_id = $1`,
			projectID,
		)
		if err != nil {
			writeJSONError(w, "failed to query templates", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		custom := make(map[string]templateResponse)
		for rows.Next() {
			var tr templateResponse
			if err := rows.Scan(&tr.TemplateType, &tr.Subject, &tr.BodyHTML); err != nil {
				continue
			}
			tr.IsCustom = true
			custom[tr.TemplateType] = tr
		}

		defaults := DefaultTemplates()
		result := make([]templateResponse, 0, len(defaults))
		for typ, def := range defaults {
			if c, ok := custom[typ]; ok {
				result = append(result, c)
			} else {
				result = append(result, templateResponse{
					TemplateType: typ,
					Subject:      def.Subject,
					BodyHTML:     def.BodyHTML,
					IsCustom:     false,
				})
			}
		}

		writeJSON(w, result, http.StatusOK)
	}
}

// HandleUpdate upserts a custom template.
func (h *TemplateHandler) HandleUpdate() http.HandlerFunc {
	validTypes := map[string]bool{
		"verification": true, "password_reset": true,
		"welcome": true, "password_changed": true,
		"magic_link": true,
	}

	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		templateType := chi.URLParam(r, "type")

		// Check if plan allows custom templates.
		if h.limitsSvc != nil {
			if err := h.limitsSvc.CheckCustomTemplates(r.Context(), projectID); err != nil {
				writeJSONError(w, err.Error(), http.StatusForbidden)
				return
			}
		}

		if !validTypes[templateType] {
			writeJSONError(w, "invalid template type", http.StatusBadRequest)
			return
		}

		var req templateUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Subject == "" || req.BodyHTML == "" {
			writeJSONError(w, "subject and body_html are required", http.StatusBadRequest)
			return
		}

		_, err := h.pool.Exec(r.Context(),
			`INSERT INTO public.email_templates (project_id, template_type, subject, body_html, updated_at)
			 VALUES ($1, $2, $3, $4, now())
			 ON CONFLICT (project_id, template_type)
			 DO UPDATE SET subject = EXCLUDED.subject, body_html = EXCLUDED.body_html, updated_at = now()`,
			projectID, templateType, req.Subject, req.BodyHTML,
		)
		if err != nil {
			slog.Error("failed to upsert template", "error", err)
			writeJSONError(w, "failed to save template", http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleDelete resets a template to default.
func (h *TemplateHandler) HandleDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		templateType := chi.URLParam(r, "type")

		_, err := h.pool.Exec(r.Context(),
			`DELETE FROM public.email_templates WHERE project_id = $1 AND template_type = $2`,
			projectID, templateType,
		)
		if err != nil {
			writeJSONError(w, "failed to delete template", http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandlePreview renders a template preview with sample data.
func (h *TemplateHandler) HandlePreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		templateType := chi.URLParam(r, "type")

		var req previewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Load project name for preview
		var projectName string
		err := h.pool.QueryRow(r.Context(),
			`SELECT name FROM public.projects WHERE id = $1`, projectID,
		).Scan(&projectName)
		if err != nil {
			projectName = "My Project"
		}

		data := TemplateData{
			UserEmail:   "user@example.com",
			ProjectName: projectName,
			ActionURL:   "https://example.com/action?token=sample-token",
			ExpiresIn:   "24 hours",
		}

		subject, body, err := RenderTemplate(templateType, req.Subject, req.BodyHTML, data)
		if err != nil {
			writeJSONError(w, "failed to render template: "+err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, map[string]string{
			"subject": subject,
			"body":    body,
		}, http.StatusOK)
	}
}

// HandleTest sends a test email to the current user.
func (h *TemplateHandler) HandleTest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.service.Configured() {
			writeJSONError(w, "email not configured", http.StatusServiceUnavailable)
			return
		}

		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		projectID := chi.URLParam(r, "id")
		templateType := chi.URLParam(r, "type")

		// Load custom template if exists
		var customSubject, customHTML string
		err := h.pool.QueryRow(r.Context(),
			`SELECT subject, body_html FROM public.email_templates WHERE project_id = $1 AND template_type = $2`,
			projectID, templateType,
		).Scan(&customSubject, &customHTML)
		if err != nil && err != pgx.ErrNoRows {
			writeJSONError(w, "failed to load template", http.StatusInternalServerError)
			return
		}

		var projectName string
		_ = h.pool.QueryRow(r.Context(),
			`SELECT name FROM public.projects WHERE id = $1`, projectID,
		).Scan(&projectName)
		if projectName == "" {
			projectName = "My Project"
		}

		data := TemplateData{
			UserEmail:   claims.Email,
			ProjectName: projectName,
			ActionURL:   "https://example.com/action?token=test-token",
			ExpiresIn:   "24 hours",
		}

		subject, body, err := RenderTemplate(templateType, customSubject, customHTML, data)
		if err != nil {
			writeJSONError(w, "render error: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := h.service.client.Send(r.Context(), claims.Email, "[TEST] "+subject, body); err != nil {
			writeJSONError(w, "failed to send test email: "+err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]string{"status": "ok", "sent_to": claims.Email}, http.StatusOK)
	}
}

// HandleEmailStatus returns whether email is configured.
func HandleEmailStatus(svc *EmailService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"configured": svc.Configured()}, http.StatusOK)
	}
}

func writeJSON(w http.ResponseWriter, v interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
