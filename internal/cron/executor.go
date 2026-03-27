package cron

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Executor runs due cron jobs on a schedule.
type Executor struct {
	svc  *CronService
	pool *pgxpool.Pool
}

// NewExecutor creates a new cron job executor.
func NewExecutor(svc *CronService, pool *pgxpool.Pool) *Executor {
	return &Executor{svc: svc, pool: pool}
}

// RunDueJobs checks all enabled cron jobs and executes those whose schedule
// matches the current time (truncated to the minute).
func (e *Executor) RunDueJobs(ctx context.Context) error {
	jobs, err := e.svc.GetDueJobs(ctx)
	if err != nil {
		return fmt.Errorf("get due jobs: %w", err)
	}

	now := time.Now().UTC()
	for _, job := range jobs {
		if !shouldRun(job.Schedule, now) {
			continue
		}

		slog.Info("executing cron job",
			"job_id", job.ID,
			"project_id", job.ProjectID,
			"name", job.Name,
			"action_type", job.ActionType,
		)

		execErr := e.executeJob(ctx, job)
		if execErr != nil {
			slog.Error("cron job execution failed",
				"job_id", job.ID,
				"name", job.Name,
				"error", execErr,
			)
		} else {
			slog.Info("cron job executed successfully",
				"job_id", job.ID,
				"name", job.Name,
			)
		}

		if recordErr := e.svc.RecordRun(ctx, job.ID, execErr); recordErr != nil {
			slog.Error("failed to record cron run",
				"job_id", job.ID,
				"error", recordErr,
			)
		}
	}

	return nil
}

// executeJob runs a single cron job action within the project's tenant schema.
func (e *Executor) executeJob(ctx context.Context, job DueJob) error {
	// Set the search_path to the tenant schema, then execute the action.
	switch job.ActionType {
	case "sql":
		_, err := e.pool.Exec(ctx,
			fmt.Sprintf("SET search_path TO %s; %s", quoteIdent(job.SchemaName), job.Action))
		if err != nil {
			return fmt.Errorf("execute sql: %w", err)
		}
	case "rpc":
		_, err := e.pool.Exec(ctx,
			fmt.Sprintf("SET search_path TO %s; SELECT %s()", quoteIdent(job.SchemaName), quoteIdent(job.Action)))
		if err != nil {
			return fmt.Errorf("execute rpc: %w", err)
		}
	default:
		return fmt.Errorf("unknown action_type: %s", job.ActionType)
	}
	return nil
}

// quoteIdent quotes an identifier to prevent SQL injection.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// shouldRun checks if a cron schedule matches the given time.
// Supports: * (any), specific numbers, and */N (step) syntax.
// Fields: minute hour day-of-month month day-of-week
func shouldRun(schedule string, t time.Time) bool {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return false
	}

	values := []int{
		t.Minute(),
		t.Hour(),
		t.Day(),
		int(t.Month()),
		int(t.Weekday()), // 0 = Sunday
	}

	for i, field := range fields {
		if !fieldMatches(field, values[i]) {
			return false
		}
	}
	return true
}

// fieldMatches checks if a single cron field matches a value.
// Supports: "*", "N", "*/N", and comma-separated lists of these.
func fieldMatches(field string, value int) bool {
	// Handle comma-separated values.
	parts := strings.Split(field, ",")
	for _, part := range parts {
		if partMatches(strings.TrimSpace(part), value) {
			return true
		}
	}
	return false
}

func partMatches(part string, value int) bool {
	if part == "*" {
		return true
	}

	// */N — step
	if strings.HasPrefix(part, "*/") {
		step, err := strconv.Atoi(part[2:])
		if err != nil || step <= 0 {
			return false
		}
		return value%step == 0
	}

	// Exact number
	n, err := strconv.Atoi(part)
	if err != nil {
		return false
	}
	return value == n
}
