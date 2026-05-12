// Package tenant provides HTTP handlers for project/tenant management.
package tenant

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/audit"
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

// reservedSlugs are subdomains the platform reserves for its own use
// (current or future). Closes #49: previously a tenant could grab a
// project slug like "www" or "admin" and collide with planned platform
// subdomains, OAuth callback paths, or brand-impersonation routes.
//
// Lowercase keys; check against strings.ToLower(slug). Add to this list
// if a new platform subdomain is planned.
var reservedSlugs = map[string]bool{
	"account":     true,
	"admin":       true,
	"api":         true,
	"app":         true,
	"auth":        true,
	"billing":     true,
	"blog":        true,
	"callback":    true,
	"cdn":         true,
	"console":     true,
	"dashboard":   true,
	"docs":        true,
	"eurobase":    true,
	"help":        true,
	"login":       true,
	"mail":        true,
	"marketing":   true,
	"oauth":       true,
	"public":      true,
	"security":    true,
	"signup":      true,
	"sso":         true,
	"static":      true,
	"status":      true,
	"superadmin":  true,
	"support":     true,
	"system":      true,
	"www":         true,
}

// slugIsReserved reports whether the slug collides with a platform-
// reserved subdomain or path.
func slugIsReserved(slug string) bool {
	return reservedSlugs[strings.ToLower(slug)]
}

// slugify converts a project name into a URL-safe slug.
//
// Reserved slugs (see slugIsReserved) get a "-app" suffix automatically
// when auto-derived. Explicitly-supplied reserved slugs are rejected at
// the handler.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = slugRe.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	if slugIsReserved(s) {
		s = s + "-app"
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

		// #70 diagnostic: log the decoded plan/region so a repro of
		// "Pro selected → plan=free in DB" can be pinned to either the
		// HTTP body, the JSON decode, or somewhere downstream. Drop once
		// the bug is closed.
		slog.Info("create project: decoded body",
			"name", req.Name,
			"slug", req.Slug,
			"region", req.Region,
			"plan", req.Plan,
			"owner_id", claims.Subject,
		)

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
			if slugIsReserved(req.Slug) {
				http.Error(w, `{"error":"this slug is reserved for platform use — please choose a different name"}`, http.StatusBadRequest)
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

		// #70 diagnostic: log right before the INSERT so we can see if
		// anything between decode and CreateProject mutated req.Plan.
		slog.Info("create project: handing to service", "plan", req.Plan, "name", req.Name)
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
		projectID := chi.URLParam(r, "id")

		claims, _, ok := RequireRole(w, r, pool, projectID, "admin")
		if !ok {
			return
		}

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

		rotated, err := svc.UpdateAuthConfig(r.Context(), projectID, claims.Subject, *body.AuthConfig)
		if err != nil {
			slog.Error("update auth config failed", "error", err, "project_id", projectID)
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not owned") {
				http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionAuthConfigUpdated,
				audit.WithTarget("project", projectID),
				audit.WithIP(r.RemoteAddr))

			// Log individual secret rotations for compliance.
			for _, provider := range rotated {
				auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
					"oauth_secret.rotated",
					audit.WithTarget("oauth_provider", provider),
					audit.WithIP(r.RemoteAddr))
			}
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
		projectID := chi.URLParam(r, "id")

		claims, _, ok := RequireRole(w, r, pool, projectID, "owner")
		if !ok {
			return
		}

		// Audit BEFORE deletion so the project_id FK still exists.
		if auditSvc := audit.FromContext(r.Context()); auditSvc != nil {
			auditSvc.Log(r.Context(), projectID, claims.Subject, claims.Email,
				audit.ActionProjectDeleted,
				audit.WithTarget("project", projectID),
				audit.WithIP(r.RemoteAddr))
		}

		if err := svc.DeleteProject(r.Context(), projectID); err != nil {
			slog.Error("delete project failed", "error", err, "project_id", projectID)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
