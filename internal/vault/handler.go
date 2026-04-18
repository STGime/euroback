package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// auditVault records a vault operation via the audit service from context.
// Noop if no audit service is wired. Never fails the caller.
func auditVault(r *http.Request, projectID, action, name string) {
	svc := audit.FromContext(r.Context())
	if svc == nil {
		return
	}
	actorID, actorEmail := audit.ActorFromContext(r.Context())
	svc.Log(r.Context(), projectID, actorID, actorEmail, action,
		audit.WithTarget("vault_secret", name),
		audit.WithIP(r.RemoteAddr),
	)
}

const (
	freeVaultLimit = 5
	proVaultLimit  = 100
)

// Routes returns a chi.Router for vault CRUD operations.
// Mounted at /platform/projects/{id}/vault
func Routes(svc *VaultService, pool *pgxpool.Pool) chi.Router {
	r := chi.NewRouter()
	r.Get("/", handlePlatformList(svc, pool))
	r.Get("/{name}", handlePlatformGet(svc, pool))
	r.Post("/", handlePlatformSet(svc, pool))
	r.Patch("/{name}", handlePlatformUpdate(svc, pool))
	r.Delete("/{name}", handlePlatformDelete(svc, pool))
	return r
}

// resolveSchema looks up the schema_name for a project the authenticated user
// has access to (via project_members). The PlatformTenantContext middleware
// already verifies membership before vault routes run.
func resolveSchema(r *http.Request, pool *pgxpool.Pool) (string, string, error) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		return "", "", fmt.Errorf("unauthorized")
	}

	projectID := chi.URLParam(r, "id")

	// Verify membership.
	var memberCount int
	pool.QueryRow(r.Context(),
		`SELECT count(*) FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, claims.Subject,
	).Scan(&memberCount)
	if memberCount == 0 {
		return "", "", fmt.Errorf("project not found")
	}

	var schemaName string
	err := pool.QueryRow(r.Context(),
		`SELECT schema_name FROM projects WHERE id = $1 AND status = 'active'`,
		projectID,
	).Scan(&schemaName)
	if err != nil {
		return "", "", fmt.Errorf("project not found")
	}
	return projectID, schemaName, nil
}

func handlePlatformList(svc *VaultService, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, schemaName, err := resolveSchema(r, pool)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		secrets, err := svc.List(r.Context(), schemaName)
		if err != nil {
			slog.Error("list vault secrets failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		jsonResponse(w, secrets, http.StatusOK)
	}
}

func handlePlatformGet(svc *VaultService, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, schemaName, err := resolveSchema(r, pool)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		name := chi.URLParam(r, "name")
		secret, err := svc.Get(r.Context(), schemaName, name)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		auditVault(r, projectID, audit.ActionVaultSecretAccessed, name)

		jsonResponse(w, secret, http.StatusOK)
	}
}

func handlePlatformSet(svc *VaultService, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, schemaName, err := resolveSchema(r, pool)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		var req struct {
			Name        string `json:"name"`
			Value       string `json:"value"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			jsonError(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.Value == "" {
			jsonError(w, "value is required", http.StatusBadRequest)
			return
		}

		// Check plan limit.
		if err := checkVaultLimit(r.Context(), svc, pool, projectID, schemaName); err != nil {
			jsonError(w, err.Error(), http.StatusForbidden)
			return
		}

		secret, err := svc.Set(r.Context(), schemaName, req.Name, req.Value, req.Description)
		if err != nil {
			slog.Error("set vault secret failed", "error", err, "project_id", projectID)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		slog.Info("vault secret set", "name", req.Name, "project_id", projectID)
		auditVault(r, projectID, audit.ActionVaultSecretSet, req.Name)
		jsonResponse(w, secret, http.StatusCreated)
	}
}

func handlePlatformUpdate(svc *VaultService, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, schemaName, err := resolveSchema(r, pool)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		name := chi.URLParam(r, "name")

		var req struct {
			Value       *string `json:"value"`
			Description *string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		secret, err := svc.Update(r.Context(), schemaName, name, req.Value, req.Description)
		if err != nil {
			slog.Error("update vault secret failed", "error", err, "project_id", projectID)
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		auditVault(r, projectID, audit.ActionVaultSecretUpdated, name)
		jsonResponse(w, secret, http.StatusOK)
	}
}

func handlePlatformDelete(svc *VaultService, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, schemaName, err := resolveSchema(r, pool)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		name := chi.URLParam(r, "name")
		if err := svc.Delete(r.Context(), schemaName, name); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		slog.Info("vault secret deleted", "name", name, "project_id", projectID)
		auditVault(r, projectID, audit.ActionVaultSecretDeleted, name)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── SDK handlers (API key authenticated) ──

// HandleSDKList returns all secret names for the project (no values).
func HandleSDKList(svc *VaultService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}
		if pc.KeyType != "secret" {
			jsonError(w, "vault access requires a secret API key (eb_sk_)", http.StatusForbidden)
			return
		}

		secrets, err := svc.List(r.Context(), pc.SchemaName)
		if err != nil {
			slog.Error("sdk list vault secrets failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		jsonResponse(w, secrets, http.StatusOK)
	}
}

// HandleSDKGet returns a decrypted secret by name.
func HandleSDKGet(svc *VaultService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}
		if pc.KeyType != "secret" {
			jsonError(w, "vault access requires a secret API key (eb_sk_)", http.StatusForbidden)
			return
		}

		name := chi.URLParam(r, "name")
		secret, err := svc.Get(r.Context(), pc.SchemaName, name)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		jsonResponse(w, secret, http.StatusOK)
	}
}

// HandleSDKSet creates or updates a secret.
func HandleSDKSet(svc *VaultService, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}
		if pc.KeyType != "secret" {
			jsonError(w, "vault access requires a secret API key (eb_sk_)", http.StatusForbidden)
			return
		}

		var req struct {
			Name        string `json:"name"`
			Value       string `json:"value"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			jsonError(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.Value == "" {
			jsonError(w, "value is required", http.StatusBadRequest)
			return
		}

		// Check plan limit.
		if err := checkVaultLimit(r.Context(), svc, pool, pc.ProjectID, pc.SchemaName); err != nil {
			jsonError(w, err.Error(), http.StatusForbidden)
			return
		}

		secret, err := svc.Set(r.Context(), pc.SchemaName, req.Name, req.Value, req.Description)
		if err != nil {
			slog.Error("sdk set vault secret failed", "error", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		jsonResponse(w, secret, http.StatusCreated)
	}
}

// HandleSDKDelete removes a secret.
func HandleSDKDelete(svc *VaultService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			jsonError(w, "missing project context", http.StatusUnauthorized)
			return
		}
		if pc.KeyType != "secret" {
			jsonError(w, "vault access requires a secret API key (eb_sk_)", http.StatusForbidden)
			return
		}

		name := chi.URLParam(r, "name")
		if err := svc.Delete(r.Context(), pc.SchemaName, name); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// checkVaultLimit enforces plan-based secret count limits.
func checkVaultLimit(ctx context.Context, svc *VaultService, pool *pgxpool.Pool, projectID, schemaName string) error {
	count, err := svc.Count(ctx, schemaName)
	if err != nil {
		return err
	}

	var plan string
	err = pool.QueryRow(ctx,
		`SELECT COALESCE(plan, 'free') FROM projects WHERE id = $1`, projectID).Scan(&plan)
	if err != nil {
		return fmt.Errorf("get project plan: %w", err)
	}

	limit := freeVaultLimit
	if plan == "pro" {
		limit = proVaultLimit
	}

	if count >= limit {
		return fmt.Errorf("%s plan is limited to %d vault secrets — upgrade to pro for more", plan, limit)
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
