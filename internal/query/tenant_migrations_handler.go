package query

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyMigrationRequest is the JSON body for
// POST /platform/projects/{id}/migrations.
type ApplyMigrationRequest struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
	SQL     string `json:"sql"`
}

// HandleTenantMigrations mounts the tenant-migration endpoints (#190):
//
//	GET  /  — applied-migration history
//	POST /  — apply one versioned migration
//
// Mounted under the platform project routes with RequireMinRole
// ("developer"). exec runs each migration under a per-tenant LOGIN role
// (see MigrationExecutor). readPool (gateway pool) serves history/lookups.
// When exec is not Enabled() (no DDL_PASSWORD_SECRET / base DSN) the apply
// endpoint fails closed with 503 — migrations never run on a privileged
// pool.
func HandleTenantMigrations(exec *MigrationExecutor, readPool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()

	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		projectID := chi.URLParam(req, "id")
		if projectID == "" {
			jsonError(w, "project ID is required", http.StatusBadRequest)
			return
		}
		list, err := ListTenantMigrations(req.Context(), readPool, projectID)
		if err != nil {
			slog.Error("list tenant migrations failed", "project_id", projectID, "error", err)
			jsonError(w, "failed to list migrations", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"migrations": list})
	})

	r.Post("/", func(w http.ResponseWriter, req *http.Request) {
		projectID := chi.URLParam(req, "id")
		if projectID == "" {
			jsonError(w, "project ID is required", http.StatusBadRequest)
			return
		}
		if !exec.Enabled() {
			// Fail closed: never run a migration on a privileged pool.
			jsonError(w, "tenant migrations are not enabled on this deployment (DDL_PASSWORD_SECRET not configured)", http.StatusServiceUnavailable)
			return
		}

		var body ApplyMigrationRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		var schemaName string
		if err := readPool.QueryRow(req.Context(),
			`SELECT schema_name FROM projects WHERE id = $1`, projectID,
		).Scan(&schemaName); err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}

		actorID, actorEmail := audit.ActorFromContext(req.Context())

		applied, err := exec.Apply(req.Context(), projectID, schemaName, body.Version, body.Name, body.SQL)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrMigrationChecksumMismatch) {
				status = http.StatusConflict
			}
			slog.Warn("tenant migration rejected or failed",
				"project_id", projectID, "version", body.Version, "error", err)
			jsonError(w, err.Error(), status)
			return
		}

		if auditSvc := audit.FromContext(req.Context()); auditSvc != nil {
			auditSvc.Log(req.Context(), projectID, actorID, actorEmail,
				"schema.migration_applied",
				audit.WithTarget("migration", body.Name),
				audit.WithMetadata(map[string]any{
					"version":  body.Version,
					"name":     body.Name,
					"checksum": MigrationChecksum(body.SQL),
					"noop":     !applied,
				}),
				audit.WithIP(req.RemoteAddr))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"applied": applied,
			"version": body.Version,
			"name":    body.Name,
		})
	})

	return r
}
