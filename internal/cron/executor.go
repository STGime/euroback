package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/functions"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FunctionInvoker wires the cron executor to the function-runner HTTP
// service so an `action_type='function'` schedule fires the named Deno
// function with the schedule's `payload` and `headers`. Both fields are
// optional — pass nil to disable function-typed schedules (they will be
// recorded as failures with a clear message).
type FunctionInvoker struct {
	RunnerURL string
	Signer    *functions.Signer
	Client    *http.Client
}

// Executor runs due cron jobs on a schedule.
type Executor struct {
	svc      *CronService
	pool     *pgxpool.Pool
	invoker  *FunctionInvoker
	location *time.Location // fallback timezone when a job's tz fails to load
}

// NewExecutor creates a new cron job executor.
func NewExecutor(svc *CronService, pool *pgxpool.Pool) *Executor {
	return &Executor{svc: svc, pool: pool, location: time.UTC}
}

// WithFunctionInvoker enables `action_type='function'` schedules by
// configuring the HTTP path to the function runner. Pass a Signer from
// the gateway/runner shared secret so the runner accepts the call. Safe
// to call zero times — function schedules then fail-fast with a clear
// message.
func (e *Executor) WithFunctionInvoker(inv FunctionInvoker) *Executor {
	if inv.Client == nil {
		inv.Client = &http.Client{Timeout: 65 * time.Second}
	}
	e.invoker = &inv
	return e
}

// RunDueJobs checks all enabled cron jobs and executes those whose schedule
// matches the current time (truncated to the minute, evaluated in the
// job's configured timezone).
func (e *Executor) RunDueJobs(ctx context.Context) error {
	jobs, err := e.svc.GetDueJobs(ctx)
	if err != nil {
		return fmt.Errorf("get due jobs: %w", err)
	}

	now := time.Now()
	for _, job := range jobs {
		loc := e.location
		if job.Timezone != "" {
			if l, err := time.LoadLocation(job.Timezone); err == nil {
				loc = l
			} else {
				slog.Warn("cron job has invalid timezone, falling back to UTC",
					"job_id", job.ID, "timezone", job.Timezone, "error", err)
			}
		}
		if !shouldRun(job.Schedule, now.In(loc)) {
			continue
		}

		slog.Info("executing cron job",
			"job_id", job.ID,
			"project_id", job.ProjectID,
			"name", job.Name,
			"action_type", job.ActionType,
		)

		// Insert a running record into cron_job_runs.
		var runID string
		insertErr := e.pool.QueryRow(ctx,
			`INSERT INTO cron_job_runs (job_id, project_id, status)
			 VALUES ($1, $2, 'running')
			 RETURNING id`,
			job.ID, job.ProjectID,
		).Scan(&runID)
		if insertErr != nil {
			slog.Error("failed to insert cron run record",
				"job_id", job.ID,
				"error", insertErr,
			)
		}

		startTime := time.Now()
		execErr := e.executeJob(ctx, job)
		durationMs := int(time.Since(startTime).Milliseconds())

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

		// Update the cron_job_runs record with results.
		if runID != "" {
			status := "success"
			var resultText *string
			var errorText *string
			if execErr != nil {
				status = "error"
				errStr := execErr.Error()
				errorText = &errStr
			} else {
				r := fmt.Sprintf("completed in %dms", durationMs)
				resultText = &r
			}
			_, updateErr := e.pool.Exec(ctx,
				`UPDATE cron_job_runs SET
					finished_at = now(),
					duration_ms = $2,
					status      = $3,
					result      = $4,
					error       = $5
				 WHERE id = $1`,
				runID, durationMs, status, resultText, errorText,
			)
			if updateErr != nil {
				slog.Error("failed to update cron run record",
					"run_id", runID,
					"error", updateErr,
				)
			}
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
//
// Closed advisory GHSA-fjjq-cqq9-q793: the previous implementation used
// `pool.Exec(ctx, "SET search_path TO X; <user-sql>")` — pgx's simple
// query protocol ran every `;`-separated statement, the gateway role had
// DML on `public.*` and every tenant schema, and a project member could
// thus issue arbitrary SQL across the platform.
//
// The current path:
//  1. Validates the action (rejects multi-statement, forbidden-schema refs).
//  2. Opens a transaction.
//  3. Sets `search_path` and `statement_timeout` via `SET LOCAL` so they
//     auto-reset on commit/rollback and never leak into other handlers
//     sharing the connection.
//  4. Executes the action via `tx.Exec` — the extended query protocol,
//     which runs only the first statement (defence-in-depth on top of
//     the multi-statement rejection).
//
// `function` action_type is dispatched out-of-band to the Deno runner
// over HTTP (same HMAC-signed path the SDK uses), so SQL-injection
// concerns don't apply there — the action is the function name.
func (e *Executor) executeJob(ctx context.Context, job DueJob) error {
	switch job.ActionType {
	case "sql":
		if err := validateCronSQLAction(job.Action); err != nil {
			return err
		}
		return e.runInTenantTx(ctx, job.SchemaName, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, job.Action); err != nil {
				return fmt.Errorf("execute sql: %w", err)
			}
			return nil
		})
	case "rpc":
		if err := validateCronRPCName(job.Action); err != nil {
			return err
		}
		return e.runInTenantTx(ctx, job.SchemaName, func(tx pgx.Tx) error {
			sql := fmt.Sprintf("SELECT %s()", quoteIdent(job.Action))
			if _, err := tx.Exec(ctx, sql); err != nil {
				return fmt.Errorf("execute rpc: %w", err)
			}
			return nil
		})
	case "function":
		return e.executeFunctionJob(ctx, job)
	default:
		return fmt.Errorf("unknown action_type: %s", job.ActionType)
	}
}

// executeFunctionJob POSTs to the function runner's /invoke endpoint with
// the same identity headers + HMAC signature the gateway uses for direct
// invocations. The runner enforces tenant isolation via X-Schema-Name on
// its side. Headers + payload from the schedule row are merged with the
// reserved identity headers — reserved keys win.
func (e *Executor) executeFunctionJob(ctx context.Context, job DueJob) error {
	if err := validateFunctionName(job.Action); err != nil {
		return err
	}
	if e.invoker == nil || e.invoker.RunnerURL == "" {
		return fmt.Errorf("function action_type requires functions runner to be configured")
	}

	// Look up function row for X-Function-ID + verify it's active.
	var fnID, fnStatus string
	err := e.pool.QueryRow(ctx,
		`SELECT id, status FROM edge_functions WHERE project_id = $1 AND name = $2`,
		job.ProjectID, job.Action).Scan(&fnID, &fnStatus)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("function %q is not deployed", job.Action)
		}
		return fmt.Errorf("look up function: %w", err)
	}
	if fnStatus != "active" {
		return fmt.Errorf("function %q is disabled", job.Action)
	}

	var body io.Reader
	if len(job.Payload) > 0 {
		body = bytes.NewReader(job.Payload)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, e.invoker.RunnerURL+"/invoke", body)
	if err != nil {
		return fmt.Errorf("build runner request: %w", err)
	}

	// Caller-supplied headers go on first; reserved identity headers
	// overwrite so a malicious schedule can't spoof project_id.
	if len(job.Headers) > 0 {
		var hdrs map[string]string
		if err := json.Unmarshal(job.Headers, &hdrs); err == nil {
			for k, v := range hdrs {
				req.Header.Set(k, v)
			}
		}
	}
	req.Header.Set("X-Project-ID", job.ProjectID)
	req.Header.Set("X-Schema-Name", job.SchemaName)
	req.Header.Set("X-Function-Name", job.Action)
	req.Header.Set("X-Function-ID", fnID)
	req.Header.Set("X-Plan", "free") // schedule-fired invocations don't carry plan; runner uses for limits only
	req.Header.Set("X-Cron-Job-ID", job.ID)
	if req.Header.Get("Content-Type") == "" && len(job.Payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.invoker.Signer != nil {
		e.invoker.Signer.Sign(req.Header, time.Now())
	}

	resp, err := e.invoker.Client.Do(req)
	if err != nil {
		return fmt.Errorf("invoke function: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("function returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// runInTenantTx wraps a cron action in a transaction with `SET LOCAL
// search_path` and `SET LOCAL statement_timeout`. Both reset on commit.
// search_path intentionally does NOT include `public` — qualified
// references are blocked by validateCronSQLAction; this stops accidental
// resolution of unqualified names (`projects`, `api_keys`) into the
// platform schema.
func (e *Executor) runInTenantTx(ctx context.Context, schemaName string, fn func(pgx.Tx) error) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", quoteIdent(schemaName))); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '30s'"); err != nil {
		return fmt.Errorf("set statement_timeout: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
