// Package tenant provides HTTP handlers for project/tenant management.
package tenant

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateProjectRequest is the JSON body for creating a new project (tenant).
type CreateProjectRequest struct {
	Name   string `json:"name"`
	Slug   string `json:"slug,omitempty"`   // optional; derived from name if empty
	Region string `json:"region,omitempty"` // defaults to "fr-par"
	Plan   string `json:"plan,omitempty"`   // defaults to "free"
}

// CreateProjectResponse is the JSON response after project creation.
type CreateProjectResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Status    string `json:"status"`
	APIURL    string `json:"api_url"`
	PublicKey string `json:"public_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
}

// ProjectListItem represents a project in the list response.
type ProjectListItem struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Slug       string          `json:"slug"`
	Region     string          `json:"region"`
	Plan       string          `json:"plan"`
	Status     string          `json:"status"`
	APIURL     string          `json:"api_url"`
	AuthConfig json.RawMessage `json:"auth_config,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

var (
	slugRe      = regexp.MustCompile(`[^a-z0-9-]+`)
	validSlugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// slugify converts a project name into a URL-safe slug.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = slugRe.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	return s
}

// HandleCreateProject returns an http.HandlerFunc that creates a new project
// (tenant) for the authenticated platform user.
//
// POST /v1/tenants
func HandleCreateProject(pool *pgxpool.Pool, svc *TenantService, limitsSvc ...*plans.LimitsService) http.HandlerFunc {
	_ = pool // pool is held by svc; kept in signature for consistency

	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			slog.Warn("create tenant called without auth claims")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Check project limit for the user's plan.
		if len(limitsSvc) > 0 && limitsSvc[0] != nil {
			if err := limitsSvc[0].CheckProjectLimit(r.Context(), claims.Subject); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
		}

		var req CreateProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			slog.Warn("invalid create tenant request body", "error", err)
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		// Validate name.
		if strings.TrimSpace(req.Name) == "" {
			http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
			return
		}

		// Validate slug if provided; otherwise it will be derived from name.
		if req.Slug != "" {
			if !validSlugRe.MatchString(req.Slug) {
				http.Error(w, `{"error":"slug must be lowercase alphanumeric with hyphens (e.g. my-app)"}`, http.StatusBadRequest)
				return
			}
		}

		// Validate region: only fr-par is supported.
		if req.Region == "" {
			req.Region = "fr-par"
		}
		if req.Region != "fr-par" {
			http.Error(w, `{"error":"region must be fr-par"}`, http.StatusBadRequest)
			return
		}

		// Default plan.
		if req.Plan == "" {
			req.Plan = "free"
		}

		project, err := svc.CreateProject(r.Context(), claims.Subject, claims.Email, req)
		if err != nil {
			slog.Error("failed to create project", "error", err, "user_id", claims.Subject)
			if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
				http.Error(w, `{"error":"This project URL is already taken. Each project gets a unique subdomain (slug.eurobase.app), so please choose a different name or slug."}`, http.StatusConflict)
				return
			}
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		slog.Info("tenant created",
			"project_id", project.ID,
			"slug", project.Slug,
			"owner_id", claims.Subject,
		)

		resp := CreateProjectResponse{
			ID:        project.ID,
			Name:      project.Name,
			Slug:      project.Slug,
			Status:    project.Status,
			APIURL:    project.APIURL,
			PublicKey: project.PublicKey,
			SecretKey: project.SecretKey,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	}
}

// HandleListProjects returns an http.HandlerFunc that lists all projects
// owned by the authenticated platform user.
//
// GET /v1/tenants
func HandleListProjects(pool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
	_ = pool

	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			slog.Warn("list tenants called without auth claims")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		projects, err := svc.ListProjects(r.Context(), claims.Subject)
		if err != nil {
			slog.Error("failed to list projects", "error", err, "user_id", claims.Subject)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		// Map to list items. AuthConfig is included so the console can render
		// the auth settings page without a second round-trip; it has already
		// been annotated (secret_set flags, no raw client_secret values) by
		// TenantService.ListProjects.
		items := make([]ProjectListItem, len(projects))
		for i, p := range projects {
			items[i] = ProjectListItem{
				ID:         p.ID,
				Name:       p.Name,
				Slug:       p.Slug,
				Region:     p.Region,
				Plan:       p.Plan,
				Status:     p.Status,
				APIURL:     p.APIURL,
				AuthConfig: p.AuthConfig,
				CreatedAt:  p.CreatedAt,
			}
		}

		slog.Debug("listed tenants", "count", len(items), "user_id", claims.Subject)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(items)
	}
}

// HandleUpdateProject handles PATCH /v1/tenants/{id} to update project settings (e.g. auth_config).
func HandleUpdateProject(pool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		projectID := chi.URLParam(r, "id")

		var body struct {
			AuthConfig *AuthConfig `json:"auth_config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		if body.AuthConfig == nil {
			http.Error(w, `{"error":"auth_config is required"}`, http.StatusBadRequest)
			return
		}

		if err := body.AuthConfig.Validate(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if err := svc.UpdateAuthConfig(r.Context(), projectID, claims.Subject, *body.AuthConfig); err != nil {
			slog.Error("update auth config failed", "error", err, "project_id", projectID)
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not owned") {
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		project, err := svc.GetProject(r.Context(), projectID)
		if err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(project)
	}
}

// HandleDeleteProject deletes a project and its tenant schema.
//
// DELETE /v1/tenants/{id}
func HandleDeleteProject(pool *pgxpool.Pool, svc *TenantService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		projectID := chi.URLParam(r, "id")

		// Verify the user owns this project.
		var ownerID string
		err := pool.QueryRow(r.Context(),
			`SELECT owner_id FROM projects WHERE id = $1`, projectID,
		).Scan(&ownerID)
		if err != nil {
			http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
			return
		}
		if ownerID != claims.Subject {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}

		if err := svc.DeleteProject(r.Context(), projectID); err != nil {
			slog.Error("delete project failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
