// Package functions provides CRUD and invocation support for Edge Functions.
package functions

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EdgeFunction represents a deployed edge function.
type EdgeFunction struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Code      string    `json:"code,omitempty"`
	VerifyJWT bool      `json:"verify_jwt"`
	EnvVars   map[string]string `json:"env_vars,omitempty"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EdgeFunctionLog represents a single function invocation log entry.
type EdgeFunctionLog struct {
	ID            string    `json:"id"`
	FunctionID    string    `json:"function_id"`
	ProjectID     string    `json:"project_id"`
	Status        int       `json:"status"`
	DurationMs    int       `json:"duration_ms"`
	Error         *string   `json:"error"`
	RequestMethod string    `json:"request_method"`
	CreatedAt     time.Time `json:"created_at"`
}

// Service provides CRUD operations for edge functions.
type Service struct {
	pool *pgxpool.Pool
}

// NewService creates a new edge functions service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

var validFnName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// List returns all edge functions for a project (code excluded for list view).
func (s *Service) List(ctx context.Context, projectID string) ([]EdgeFunction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, verify_jwt, status, version, created_at, updated_at
		 FROM edge_functions
		 WHERE project_id = $1
		 ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list edge functions: %w", err)
	}
	defer rows.Close()

	var fns []EdgeFunction
	for rows.Next() {
		var f EdgeFunction
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Name, &f.VerifyJWT, &f.Status, &f.Version, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan edge function: %w", err)
		}
		fns = append(fns, f)
	}

	if fns == nil {
		fns = []EdgeFunction{}
	}
	return fns, nil
}

// Get returns a single edge function by name, including code.
func (s *Service) Get(ctx context.Context, projectID, name string) (*EdgeFunction, error) {
	var f EdgeFunction
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, name, code, verify_jwt, COALESCE(env_vars, '{}'), status, version, created_at, updated_at
		 FROM edge_functions
		 WHERE project_id = $1 AND name = $2`, projectID, name,
	).Scan(&f.ID, &f.ProjectID, &f.Name, &f.Code, &f.VerifyJWT, &f.EnvVars, &f.Status, &f.Version, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get edge function %q: %w", name, err)
	}
	return &f, nil
}

// GetByID returns a single edge function by ID, including code.
func (s *Service) GetByID(ctx context.Context, id string) (*EdgeFunction, error) {
	var f EdgeFunction
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, name, code, verify_jwt, COALESCE(env_vars, '{}'), status, version, created_at, updated_at
		 FROM edge_functions
		 WHERE id = $1`, id,
	).Scan(&f.ID, &f.ProjectID, &f.Name, &f.Code, &f.VerifyJWT, &f.EnvVars, &f.Status, &f.Version, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get edge function by id: %w", err)
	}
	return &f, nil
}

// CreateRequest is the payload for creating an edge function.
type CreateRequest struct {
	Name      string `json:"name"`
	Code      string `json:"code"`
	VerifyJWT *bool  `json:"verify_jwt,omitempty"`
}

// Create creates a new edge function.
func (s *Service) Create(ctx context.Context, projectID string, req CreateRequest) (*EdgeFunction, error) {
	if !validFnName.MatchString(req.Name) {
		return nil, fmt.Errorf("invalid function name: must start with a lowercase letter, contain only lowercase letters, numbers, hyphens, and underscores, and be 1-63 characters")
	}

	if req.Code == "" {
		return nil, fmt.Errorf("code is required")
	}

	verifyJWT := true
	if req.VerifyJWT != nil {
		verifyJWT = *req.VerifyJWT
	}

	var f EdgeFunction
	err := s.pool.QueryRow(ctx,
		`INSERT INTO edge_functions (project_id, name, code, verify_jwt)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, project_id, name, code, verify_jwt, status, version, created_at, updated_at`,
		projectID, req.Name, req.Code, verifyJWT,
	).Scan(&f.ID, &f.ProjectID, &f.Name, &f.Code, &f.VerifyJWT, &f.Status, &f.Version, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create edge function: %w", err)
	}

	slog.Info("edge function created", "project_id", projectID, "name", req.Name)
	return &f, nil
}

// UpdateRequest is the payload for updating an edge function.
type UpdateRequest struct {
	Code      *string           `json:"code,omitempty"`
	VerifyJWT *bool             `json:"verify_jwt,omitempty"`
	Status    *string           `json:"status,omitempty"`
	EnvVars   map[string]string `json:"env_vars,omitempty"`
}

// Update updates an existing edge function. Any non-nil field is updated.
func (s *Service) Update(ctx context.Context, projectID, name string, req UpdateRequest) (*EdgeFunction, error) {
	// Build dynamic update.
	existing, err := s.Get(ctx, projectID, name)
	if err != nil {
		return nil, err
	}

	code := existing.Code
	verifyJWT := existing.VerifyJWT
	status := existing.Status
	envVars := existing.EnvVars
	bumpVersion := false

	if req.Code != nil {
		code = *req.Code
		bumpVersion = true
	}
	if req.VerifyJWT != nil {
		verifyJWT = *req.VerifyJWT
	}
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "disabled" {
			return nil, fmt.Errorf("invalid status: must be 'active' or 'disabled'")
		}
		status = *req.Status
	}
	if req.EnvVars != nil {
		envVars = req.EnvVars
	}

	version := existing.Version
	if bumpVersion {
		// Save current code as a version before overwriting.
		if saveErr := s.SaveVersion(ctx, existing.ID, existing.Version, existing.Code); saveErr != nil {
			slog.Warn("failed to save function version", "error", saveErr)
		}
		version++
	}

	var f EdgeFunction
	err = s.pool.QueryRow(ctx,
		`UPDATE edge_functions
		 SET code = $3, verify_jwt = $4, status = $5, env_vars = $6, version = $7, updated_at = now()
		 WHERE project_id = $1 AND name = $2
		 RETURNING id, project_id, name, code, verify_jwt, status, version, created_at, updated_at`,
		projectID, name, code, verifyJWT, status, envVars, version,
	).Scan(&f.ID, &f.ProjectID, &f.Name, &f.Code, &f.VerifyJWT, &f.Status, &f.Version, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update edge function: %w", err)
	}

	slog.Info("edge function updated", "project_id", projectID, "name", name, "version", version)
	return &f, nil
}

// Delete removes an edge function.
func (s *Service) Delete(ctx context.Context, projectID, name string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM edge_functions WHERE project_id = $1 AND name = $2`,
		projectID, name)
	if err != nil {
		return fmt.Errorf("delete edge function: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("function %q not found", name)
	}

	slog.Info("edge function deleted", "project_id", projectID, "name", name)
	return nil
}

// GetLogs returns recent execution logs for a function.
func (s *Service) GetLogs(ctx context.Context, projectID, name string, limit int) ([]EdgeFunctionLog, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := s.pool.Query(ctx,
		`SELECT l.id, l.function_id, l.project_id, l.status, l.duration_ms, l.error, l.request_method, l.created_at
		 FROM edge_function_logs l
		 JOIN edge_functions f ON f.id = l.function_id
		 WHERE f.project_id = $1 AND f.name = $2
		 ORDER BY l.created_at DESC
		 LIMIT $3`, projectID, name, limit)
	if err != nil {
		return nil, fmt.Errorf("get function logs: %w", err)
	}
	defer rows.Close()

	var logs []EdgeFunctionLog
	for rows.Next() {
		var log EdgeFunctionLog
		if err := rows.Scan(&log.ID, &log.FunctionID, &log.ProjectID, &log.Status, &log.DurationMs, &log.Error, &log.RequestMethod, &log.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan function log: %w", err)
		}
		logs = append(logs, log)
	}
	if logs == nil {
		logs = []EdgeFunctionLog{}
	}
	return logs, nil
}

// LogInvocation records a function invocation in the logs table.
func (s *Service) LogInvocation(ctx context.Context, functionID, projectID string, status, durationMs int, errMsg string, method string) {
	var errPtr *string
	if errMsg != "" {
		errPtr = &errMsg
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO edge_function_logs (function_id, project_id, status, duration_ms, error, request_method)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		functionID, projectID, status, durationMs, errPtr, method)
	if err != nil {
		slog.Error("failed to log function invocation", "function_id", functionID, "error", err)
	}
}

// Count returns the number of edge functions for a project.
func (s *Service) Count(ctx context.Context, projectID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM edge_functions WHERE project_id = $1`, projectID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count edge functions: %w", err)
	}
	return count, nil
}

// ── Versioning ──

// EdgeFunctionVersion represents a historical version of a function's code.
type EdgeFunctionVersion struct {
	ID         string    `json:"id"`
	FunctionID string    `json:"function_id"`
	Version    int       `json:"version"`
	Code       string    `json:"code"`
	CreatedAt  time.Time `json:"created_at"`
}

// SaveVersion stores the current code as a version before an update.
func (s *Service) SaveVersion(ctx context.Context, functionID string, version int, code string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO edge_function_versions (function_id, version, code)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (function_id, version) DO NOTHING`,
		functionID, version, code)
	if err != nil {
		return fmt.Errorf("save function version: %w", err)
	}
	return nil
}

// ListVersions returns the version history for a function.
func (s *Service) ListVersions(ctx context.Context, projectID, name string) ([]EdgeFunctionVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT v.id, v.function_id, v.version, v.code, v.created_at
		 FROM edge_function_versions v
		 JOIN edge_functions f ON f.id = v.function_id
		 WHERE f.project_id = $1 AND f.name = $2
		 ORDER BY v.version DESC`, projectID, name)
	if err != nil {
		return nil, fmt.Errorf("list function versions: %w", err)
	}
	defer rows.Close()

	var versions []EdgeFunctionVersion
	for rows.Next() {
		var v EdgeFunctionVersion
		if err := rows.Scan(&v.ID, &v.FunctionID, &v.Version, &v.Code, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan function version: %w", err)
		}
		versions = append(versions, v)
	}
	if versions == nil {
		versions = []EdgeFunctionVersion{}
	}
	return versions, nil
}

// Rollback restores a function's code from a previous version.
func (s *Service) Rollback(ctx context.Context, projectID, name string, version int) (*EdgeFunction, error) {
	// Get the function.
	fn, err := s.Get(ctx, projectID, name)
	if err != nil {
		return nil, err
	}

	// Get the target version's code.
	var code string
	err = s.pool.QueryRow(ctx,
		`SELECT code FROM edge_function_versions
		 WHERE function_id = $1 AND version = $2`,
		fn.ID, version,
	).Scan(&code)
	if err != nil {
		return nil, fmt.Errorf("version %d not found: %w", version, err)
	}

	// Save current code as a new version before rollback.
	if err := s.SaveVersion(ctx, fn.ID, fn.Version, fn.Code); err != nil {
		slog.Warn("failed to save current version before rollback", "error", err)
	}

	// Update the function with the rolled-back code.
	newVersion := fn.Version + 1
	var updated EdgeFunction
	err = s.pool.QueryRow(ctx,
		`UPDATE edge_functions
		 SET code = $3, version = $4, updated_at = now()
		 WHERE project_id = $1 AND name = $2
		 RETURNING id, project_id, name, code, verify_jwt, status, version, created_at, updated_at`,
		projectID, name, code, newVersion,
	).Scan(&updated.ID, &updated.ProjectID, &updated.Name, &updated.Code, &updated.VerifyJWT, &updated.Status, &updated.Version, &updated.CreatedAt, &updated.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("rollback edge function: %w", err)
	}

	slog.Info("edge function rolled back", "project_id", projectID, "name", name, "to_version", version, "new_version", newVersion)
	return &updated, nil
}

// ── Metrics ──

// FunctionMetrics contains aggregated invocation statistics.
type FunctionMetrics struct {
	TotalInvocations int     `json:"total_invocations"`
	ErrorCount       int     `json:"error_count"`
	ErrorRate        float64 `json:"error_rate"`
	AvgDurationMs    float64 `json:"avg_duration_ms"`
	P95DurationMs    float64 `json:"p95_duration_ms"`
	Period           string  `json:"period"`
}

// GetMetrics returns aggregated invocation stats for a function.
func (s *Service) GetMetrics(ctx context.Context, projectID, name, period string) (*FunctionMetrics, error) {
	// Parse period to interval.
	var interval string
	switch period {
	case "7d":
		interval = "7 days"
	case "30d":
		interval = "30 days"
	default:
		interval = "24 hours"
		period = "24h"
	}

	var m FunctionMetrics
	m.Period = period

	err := s.pool.QueryRow(ctx,
		`SELECT
			COALESCE(count(*), 0),
			COALESCE(count(*) FILTER (WHERE l.status >= 500), 0),
			COALESCE(avg(l.duration_ms), 0),
			COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY l.duration_ms), 0)
		 FROM edge_function_logs l
		 JOIN edge_functions f ON f.id = l.function_id
		 WHERE f.project_id = $1 AND f.name = $2
		   AND l.created_at >= now() - $3::interval`,
		projectID, name, interval,
	).Scan(&m.TotalInvocations, &m.ErrorCount, &m.AvgDurationMs, &m.P95DurationMs)
	if err != nil {
		return nil, fmt.Errorf("get function metrics: %w", err)
	}

	if m.TotalInvocations > 0 {
		m.ErrorRate = float64(m.ErrorCount) / float64(m.TotalInvocations) * 100
	}

	return &m, nil
}
