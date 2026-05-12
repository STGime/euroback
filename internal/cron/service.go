// Package cron provides scheduled job management for Eurobase projects.
package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a cron job lookup misses.
var ErrNotFound = errors.New("cron job not found")

// ErrNameAlreadyExists is returned by Create when (project_id, name) is
// already taken. The SDK surfaces this as an `already_exists` discriminator
// per #112 — callers should `update()` to change an existing schedule.
var ErrNameAlreadyExists = errors.New("schedule with this name already exists")

// CronJob represents a scheduled job configured by a project owner.
type CronJob struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	Name        string          `json:"name"`
	Schedule    string          `json:"schedule"`
	Timezone    string          `json:"timezone"`
	ActionType  string          `json:"action_type"`
	Action      string          `json:"action"`
	Description *string         `json:"description"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Headers     json.RawMessage `json:"headers,omitempty"`
	Enabled     bool            `json:"enabled"`
	LastRunAt   *time.Time      `json:"last_run_at"`
	LastError   *string         `json:"last_error"`
	RunCount    int             `json:"run_count"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// DueJob extends CronJob with the tenant schema name + plan needed for
// execution. Plan flows through to the runner's X-Plan header so
// pro-plan projects get pro-tier limits on schedule-fired invocations
// (review feedback on PR #113).
type DueJob struct {
	CronJob
	SchemaName string
	Plan       string
}

// CreateCronJobRequest is the payload for creating a new cron job.
type CreateCronJobRequest struct {
	Name        string          `json:"name"`
	Schedule    string          `json:"schedule"`
	Timezone    string          `json:"timezone,omitempty"`
	ActionType  string          `json:"action_type"`
	Action      string          `json:"action"`
	Description *string         `json:"description,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Headers     json.RawMessage `json:"headers,omitempty"`
	Enabled     *bool           `json:"enabled,omitempty"`
}

// Validate checks that all required fields are present and valid.
func (r *CreateCronJobRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(r.Action) == "" {
		return fmt.Errorf("action is required")
	}
	if r.ActionType != "sql" && r.ActionType != "rpc" && r.ActionType != "function" {
		return fmt.Errorf("action_type must be 'sql', 'rpc', or 'function'")
	}
	if r.Timezone != "" {
		if _, err := time.LoadLocation(r.Timezone); err != nil {
			return fmt.Errorf("invalid timezone %q: %w", r.Timezone, err)
		}
	}
	return validateCronSchedule(r.Schedule)
}

// UpdateCronJobRequest is the payload for updating a cron job.
type UpdateCronJobRequest struct {
	Name        *string         `json:"name,omitempty"`
	Schedule    *string         `json:"schedule,omitempty"`
	Timezone    *string         `json:"timezone,omitempty"`
	ActionType  *string         `json:"action_type,omitempty"`
	Action      *string         `json:"action,omitempty"`
	Description *string         `json:"description,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Headers     json.RawMessage `json:"headers,omitempty"`
	Enabled     *bool           `json:"enabled,omitempty"`
}

// CronService handles CRUD operations for cron jobs.
type CronService struct {
	pool *pgxpool.Pool
}

// NewCronService creates a new CronService.
func NewCronService(pool *pgxpool.Pool) *CronService {
	return &CronService{pool: pool}
}

const cronJobColumns = `id, project_id, name, schedule, timezone, action_type, action,
		description, payload, headers, enabled,
		last_run_at, last_error, run_count, created_at, updated_at`

func scanCronJob(row pgx.Row, j *CronJob) error {
	return row.Scan(&j.ID, &j.ProjectID, &j.Name, &j.Schedule, &j.Timezone,
		&j.ActionType, &j.Action, &j.Description, &j.Payload, &j.Headers,
		&j.Enabled, &j.LastRunAt, &j.LastError, &j.RunCount,
		&j.CreatedAt, &j.UpdatedAt)
}

// List returns all cron jobs for a project.
func (s *CronService) List(ctx context.Context, projectID string) ([]CronJob, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+cronJobColumns+`
		 FROM cron_jobs WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query cron jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]CronJob, 0)
	for rows.Next() {
		var j CronJob
		if err := scanCronJob(rows, &j); err != nil {
			return nil, fmt.Errorf("scan cron job: %w", err)
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// GetByName looks up a cron job by its (project_id, name) pair. The SDK
// addresses schedules by their stable name; the UUID is server-allocated
// and opaque.
func (s *CronService) GetByName(ctx context.Context, projectID, name string) (*CronJob, error) {
	var j CronJob
	err := scanCronJob(s.pool.QueryRow(ctx,
		`SELECT `+cronJobColumns+`
		 FROM cron_jobs WHERE project_id = $1 AND name = $2`,
		projectID, name), &j)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get cron job by name: %w", err)
	}
	return &j, nil
}

// Create inserts a new cron job for a project. Returns ErrNameAlreadyExists
// if (project_id, name) collides — callers are expected to use UpdateByName
// instead. Idempotency contract per #112.
func (s *CronService) Create(ctx context.Context, projectID string, req CreateCronJobRequest) (*CronJob, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.ActionType == "function" {
		if err := validateFunctionName(req.Action); err != nil {
			return nil, err
		}
	}

	tz := req.Timezone
	if tz == "" {
		tz = "UTC"
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	var j CronJob
	err := scanCronJob(s.pool.QueryRow(ctx,
		`INSERT INTO cron_jobs (project_id, name, schedule, timezone, action_type, action,
		                        description, payload, headers, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING `+cronJobColumns,
		projectID, req.Name, req.Schedule, tz, req.ActionType, req.Action,
		req.Description, nullableJSON(req.Payload), nullableJSON(req.Headers), enabled,
	), &j)
	if err != nil {
		// 23505 is unique_violation — fires on cron_jobs_project_name_uq.
		if isUniqueViolation(err) {
			return nil, ErrNameAlreadyExists
		}
		return nil, fmt.Errorf("insert cron job: %w", err)
	}

	slog.Info("cron job created", "job_id", j.ID, "project_id", projectID, "name", req.Name)
	return &j, nil
}

// Update modifies an existing cron job by its UUID.
func (s *CronService) Update(ctx context.Context, projectID, jobID string, req UpdateCronJobRequest) (*CronJob, error) {
	return s.updateBy(ctx, "id = $1 AND project_id = $2", []any{jobID, projectID}, req)
}

// UpdateByName modifies an existing cron job by its (project_id, name) pair.
func (s *CronService) UpdateByName(ctx context.Context, projectID, name string, req UpdateCronJobRequest) (*CronJob, error) {
	return s.updateBy(ctx, "name = $1 AND project_id = $2", []any{name, projectID}, req)
}

func (s *CronService) updateBy(ctx context.Context, whereClause string, whereArgs []any, req UpdateCronJobRequest) (*CronJob, error) {
	if req.Schedule != nil {
		if err := validateCronSchedule(*req.Schedule); err != nil {
			return nil, err
		}
	}
	if req.Timezone != nil && *req.Timezone != "" {
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			return nil, fmt.Errorf("invalid timezone %q: %w", *req.Timezone, err)
		}
	}
	if req.ActionType != nil && *req.ActionType != "sql" && *req.ActionType != "rpc" && *req.ActionType != "function" {
		return nil, fmt.Errorf("action_type must be 'sql', 'rpc', or 'function'")
	}

	args := append([]any{}, whereArgs...)
	args = append(args,
		req.Name, req.Schedule, req.Timezone, req.ActionType, req.Action,
		req.Description, nullableJSON(req.Payload), nullableJSON(req.Headers), req.Enabled,
	)
	q := `UPDATE cron_jobs SET
			name        = COALESCE($3, name),
			schedule    = COALESCE($4, schedule),
			timezone    = COALESCE($5, timezone),
			action_type = COALESCE($6, action_type),
			action      = COALESCE($7, action),
			description = COALESCE($8, description),
			payload     = COALESCE($9, payload),
			headers     = COALESCE($10, headers),
			enabled     = COALESCE($11, enabled),
			updated_at  = now()
		 WHERE ` + whereClause + `
		 RETURNING ` + cronJobColumns
	var j CronJob
	if err := scanCronJob(s.pool.QueryRow(ctx, q, args...), &j); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update cron job: %w", err)
	}
	slog.Info("cron job updated", "job_id", j.ID, "project_id", j.ProjectID, "name", j.Name)
	return &j, nil
}

// Delete removes a cron job by UUID.
func (s *CronService) Delete(ctx context.Context, projectID, jobID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM cron_jobs WHERE id = $1 AND project_id = $2`, jobID, projectID)
	if err != nil {
		return fmt.Errorf("delete cron job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	slog.Info("cron job deleted", "job_id", jobID, "project_id", projectID)
	return nil
}

// DeleteByName removes a cron job by (project_id, name).
func (s *CronService) DeleteByName(ctx context.Context, projectID, name string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM cron_jobs WHERE name = $1 AND project_id = $2`, name, projectID)
	if err != nil {
		return fmt.Errorf("delete cron job by name: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	slog.Info("cron job deleted", "name", name, "project_id", projectID)
	return nil
}

// GetDueJobs returns all enabled cron jobs for active projects, including
// the schema name and plan. Plan is needed so the executor can pass the
// correct X-Plan to the function runner for pro-tier limits.
func (s *CronService) GetDueJobs(ctx context.Context) ([]DueJob, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT cj.id, cj.project_id, cj.name, cj.schedule, cj.timezone, cj.action_type,
		        cj.action, cj.description, cj.payload, cj.headers, cj.enabled,
		        cj.last_run_at, cj.last_error, cj.run_count,
		        cj.created_at, cj.updated_at, p.schema_name, COALESCE(p.plan, 'free')
		 FROM cron_jobs cj
		 JOIN projects p ON cj.project_id = p.id
		 WHERE cj.enabled = true AND p.status = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("query due jobs: %w", err)
	}
	defer rows.Close()

	var jobs []DueJob
	for rows.Next() {
		var d DueJob
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Schedule, &d.Timezone,
			&d.ActionType, &d.Action, &d.Description, &d.Payload, &d.Headers,
			&d.Enabled, &d.LastRunAt, &d.LastError, &d.RunCount,
			&d.CreatedAt, &d.UpdatedAt, &d.SchemaName, &d.Plan); err != nil {
			return nil, fmt.Errorf("scan due job: %w", err)
		}
		jobs = append(jobs, d)
	}
	return jobs, nil
}

// RecordRun updates a cron job after execution.
func (s *CronService) RecordRun(ctx context.Context, jobID string, runErr error) error {
	var errMsg *string
	if runErr != nil {
		s := runErr.Error()
		errMsg = &s
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE cron_jobs SET
			last_run_at = now(),
			run_count   = run_count + 1,
			last_error  = $2,
			updated_at  = now()
		 WHERE id = $1`, jobID, errMsg)
	if err != nil {
		return fmt.Errorf("record cron run: %w", err)
	}
	return nil
}

// CronJobRun represents a single execution record for a cron job.
type CronJobRun struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	ProjectID  string     `json:"project_id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	DurationMs *int       `json:"duration_ms"`
	Status     string     `json:"status"`
	Result     *string    `json:"result"`
	Error      *string    `json:"error"`
}

// ListRuns returns the most recent execution records for a cron job.
func (s *CronService) ListRuns(ctx context.Context, jobID string, limit int) ([]CronJobRun, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, job_id, project_id, started_at, finished_at, duration_ms, status, result, error
		 FROM cron_job_runs WHERE job_id = $1 ORDER BY started_at DESC LIMIT $2`, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("query cron job runs: %w", err)
	}
	defer rows.Close()

	runs := make([]CronJobRun, 0)
	for rows.Next() {
		var r CronJobRun
		if err := rows.Scan(&r.ID, &r.JobID, &r.ProjectID, &r.StartedAt, &r.FinishedAt,
			&r.DurationMs, &r.Status, &r.Result, &r.Error); err != nil {
			return nil, fmt.Errorf("scan cron job run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, nil
}

// Count returns the number of cron jobs for a project (for plan limit enforcement).
func (s *CronService) Count(ctx context.Context, projectID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM cron_jobs WHERE project_id = $1`, projectID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count cron jobs: %w", err)
	}
	return count, nil
}

// validateCronSchedule checks that a cron expression has 5 space-separated fields.
func validateCronSchedule(schedule string) error {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return fmt.Errorf("invalid cron schedule: must have 5 fields (minute hour day-of-month month day-of-week)")
	}
	return nil
}

// validateFunctionName mirrors validateCronRPCName — function names are
// safe identifiers so they can be used in the runner URL path. The
// function service does its own DB-side lookup, so this is just shape
// guard, not auth. Hyphens are allowed (`purge-expired-images`) since
// edge functions follow kebab-case.
func validateFunctionName(name string) error {
	if !validFunctionNameRe.MatchString(strings.TrimSpace(name)) {
		return errors.New("invalid function name (use letters, digits, underscores, hyphens)")
	}
	return nil
}

// nullableJSON returns nil for empty raw JSON so the COALESCE in
// updateBy() preserves the existing column value instead of overwriting
// it with the literal JSON null.
func nullableJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// isUniqueViolation reports whether err is a pg unique_violation (23505).
// Wrapped errors are unwrapped one level (we use fmt.Errorf("%w") above).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	type pgErr interface {
		SQLState() string
	}
	if pe, ok := err.(pgErr); ok {
		return pe.SQLState() == "23505"
	}
	var pe pgErr
	if errors.As(err, &pe) {
		return pe.SQLState() == "23505"
	}
	return false
}
