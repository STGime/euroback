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
// ("developer") + withDeveloperRole, on the developer pool — the same
// trust plane as the schema DDL endpoints. ddlPool runs the migration
// (SET LOCAL ROLE eurobase_migrator); readPool serves lookups/history.
func HandleTenantMigrations(ddlPool, readPool *pgxpool.Pool) http.Handler {
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

		applied, err := ApplyTenantMigration(req.Context(), ddlPool, projectID, schemaName, body.Version, body.Name, body.SQL, actorEmail)
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
