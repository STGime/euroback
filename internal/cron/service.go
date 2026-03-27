// Package cron provides scheduled job management for Eurobase projects.
package cron

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CronJob represents a scheduled job configured by a project owner.
type CronJob struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Name       string     `json:"name"`
	Schedule   string     `json:"schedule"`
	ActionType string     `json:"action_type"`
	Action     string     `json:"action"`
	Enabled    bool       `json:"enabled"`
	LastRunAt  *time.Time `json:"last_run_at"`
	LastError  *string    `json:"last_error"`
	RunCount   int        `json:"run_count"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// DueJob extends CronJob with the tenant schema name for execution.
type DueJob struct {
	CronJob
	SchemaName string
}

// CreateCronJobRequest is the payload for creating a new cron job.
type CreateCronJobRequest struct {
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	ActionType string `json:"action_type"`
	Action     string `json:"action"`
}

// Validate checks that all required fields are present and valid.
func (r *CreateCronJobRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(r.Action) == "" {
		return fmt.Errorf("action is required")
	}
	if r.ActionType != "sql" && r.ActionType != "rpc" {
		return fmt.Errorf("action_type must be 'sql' or 'rpc'")
	}
	return validateCronSchedule(r.Schedule)
}

// UpdateCronJobRequest is the payload for updating a cron job.
type UpdateCronJobRequest struct {
	Name       *string `json:"name,omitempty"`
	Schedule   *string `json:"schedule,omitempty"`
	ActionType *string `json:"action_type,omitempty"`
	Action     *string `json:"action,omitempty"`
	Enabled    *bool   `json:"enabled,omitempty"`
}

// CronService handles CRUD operations for cron jobs.
type CronService struct {
	pool *pgxpool.Pool
}

// NewCronService creates a new CronService.
func NewCronService(pool *pgxpool.Pool) *CronService {
	return &CronService{pool: pool}
}

// List returns all cron jobs for a project.
func (s *CronService) List(ctx context.Context, projectID string) ([]CronJob, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, project_id, name, schedule, action_type, action, enabled,
		        last_run_at, last_error, run_count, created_at, updated_at
		 FROM cron_jobs WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query cron jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]CronJob, 0)
	for rows.Next() {
		var j CronJob
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.Name, &j.Schedule, &j.ActionType,
			&j.Action, &j.Enabled, &j.LastRunAt, &j.LastError, &j.RunCount,
			&j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan cron job: %w", err)
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// Create inserts a new cron job for a project.
func (s *CronService) Create(ctx context.Context, projectID string, req CreateCronJobRequest) (*CronJob, error) {
	// Validate inputs.
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(req.Action) == "" {
		return nil, fmt.Errorf("action is required")
	}
	if req.ActionType != "sql" && req.ActionType != "rpc" {
		return nil, fmt.Errorf("action_type must be 'sql' or 'rpc'")
	}
	if err := validateCronSchedule(req.Schedule); err != nil {
		return nil, err
	}

	var j CronJob
	err := s.pool.QueryRow(ctx,
		`INSERT INTO cron_jobs (project_id, name, schedule, action_type, action)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, project_id, name, schedule, action_type, action, enabled,
		           last_run_at, last_error, run_count, created_at, updated_at`,
		projectID, req.Name, req.Schedule, req.ActionType, req.Action,
	).Scan(&j.ID, &j.ProjectID, &j.Name, &j.Schedule, &j.ActionType,
		&j.Action, &j.Enabled, &j.LastRunAt, &j.LastError, &j.RunCount,
		&j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert cron job: %w", err)
	}

	slog.Info("cron job created", "job_id", j.ID, "project_id", projectID, "name", req.Name)
	return &j, nil
}

// Update modifies an existing cron job.
func (s *CronService) Update(ctx context.Context, projectID, jobID string, req UpdateCronJobRequest) (*CronJob, error) {
	// Validate schedule if provided.
	if req.Schedule != nil {
		if err := validateCronSchedule(*req.Schedule); err != nil {
			return nil, err
		}
	}
	if req.ActionType != nil && *req.ActionType != "sql" && *req.ActionType != "rpc" {
		return nil, fmt.Errorf("action_type must be 'sql' or 'rpc'")
	}

	var j CronJob
	err := s.pool.QueryRow(ctx,
		`UPDATE cron_jobs SET
			name        = COALESCE($3, name),
			schedule    = COALESCE($4, schedule),
			action_type = COALESCE($5, action_type),
			action      = COALESCE($6, action),
			enabled     = COALESCE($7, enabled),
			updated_at  = now()
		 WHERE id = $1 AND project_id = $2
		 RETURNING id, project_id, name, schedule, action_type, action, enabled,
		           last_run_at, last_error, run_count, created_at, updated_at`,
		jobID, projectID, req.Name, req.Schedule, req.ActionType, req.Action, req.Enabled,
	).Scan(&j.ID, &j.ProjectID, &j.Name, &j.Schedule, &j.ActionType,
		&j.Action, &j.Enabled, &j.LastRunAt, &j.LastError, &j.RunCount,
		&j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("cron job not found")
		}
		return nil, fmt.Errorf("update cron job: %w", err)
	}

	slog.Info("cron job updated", "job_id", jobID, "project_id", projectID)
	return &j, nil
}

// Delete removes a cron job.
func (s *CronService) Delete(ctx context.Context, projectID, jobID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM cron_jobs WHERE id = $1 AND project_id = $2`, jobID, projectID)
	if err != nil {
		return fmt.Errorf("delete cron job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("cron job not found")
	}
	slog.Info("cron job deleted", "job_id", jobID, "project_id", projectID)
	return nil
}

// GetDueJobs returns all enabled cron jobs for active projects, including the schema name.
func (s *CronService) GetDueJobs(ctx context.Context) ([]DueJob, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT cj.id, cj.project_id, cj.name, cj.schedule, cj.action_type, cj.action,
		        cj.enabled, cj.last_run_at, cj.last_error, cj.run_count,
		        cj.created_at, cj.updated_at, p.schema_name
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
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Schedule, &d.ActionType,
			&d.Action, &d.Enabled, &d.LastRunAt, &d.LastError, &d.RunCount,
			&d.CreatedAt, &d.UpdatedAt, &d.SchemaName); err != nil {
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
