package workers

// River worker for #845: runs one edge function for one database event
// (public.function_triggers). Enqueued by internal/events.Emitter on the
// gateway; invoked through the functions runner the same way as a cron
// `function` schedule (cron.FunctionInvoker), so the function runs with
// the service identity and gets the event envelope as its request body.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eurobase/euroback/internal/cron"
	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// InvokeFunctionTriggerWorker handles jobs.InvokeFunctionTriggerArgs.
type InvokeFunctionTriggerWorker struct {
	river.WorkerDefaults[jobs.InvokeFunctionTriggerArgs]
	Pool *pgxpool.Pool
	// Invoker reaches the functions runner; nil when FUNCTION_RUNNER_URL
	// isn't set on the worker (triggers then fail without retrying).
	Invoker *cron.FunctionInvoker
}

// Timeout: the runner allows a function 60 s.
func (w *InvokeFunctionTriggerWorker) Timeout(*river.Job[jobs.InvokeFunctionTriggerArgs]) time.Duration {
	return 75 * time.Second
}

func (w *InvokeFunctionTriggerWorker) Work(ctx context.Context, job *river.Job[jobs.InvokeFunctionTriggerArgs]) error {
	a := job.Args
	var (
		projectID, schema, plan, fnID, fnName, fnStatus string
		fnVersion                                       int
		enabled                                         *bool
	)
	err := w.Pool.QueryRow(ctx,
		`SELECT p.id::text, p.schema_name, COALESCE(p.plan, ''), f.id::text, f.name, f.status, f.version, t.enabled
		   FROM public.function_triggers t
		   JOIN public.edge_functions f ON f.id = t.function_id AND f.project_id = t.project_id
		   JOIN public.projects p ON p.id = t.project_id
		  WHERE t.id = $1::uuid`, a.TriggerID,
	).Scan(&projectID, &schema, &plan, &fnID, &fnName, &fnStatus, &fnVersion, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // trigger or function deleted since the event
	}
	if err != nil {
		return fmt.Errorf("load function trigger %s: %w", a.TriggerID, err)
	}
	if (enabled != nil && !*enabled) || fnStatus != "active" {
		return nil
	}
	if w.Invoker == nil || w.Invoker.RunnerURL == "" {
		return river.JobCancel(errors.New("FUNCTION_RUNNER_URL is not set on the worker — function triggers can't run"))
	}

	err = w.Invoker.Invoke(ctx, cron.FunctionCall{
		ProjectID:       projectID,
		SchemaName:      schema,
		Plan:            plan,
		FunctionID:      fnID,
		FunctionName:    fnName,
		FunctionVersion: fnVersion,
		Body:            a.Body,
		Reserved: map[string]string{
			"X-Function-Trigger-ID": a.TriggerID,
			"X-Eurobase-Delivery":   a.EventID,
		},
	})
	var status *cron.ErrFunctionStatus
	if errors.As(err, &status) && status.Status < 500 && status.Status != 408 && status.Status != 429 {
		// The function (or the runner) refused the call: a retry would
		// get the same answer.
		return river.JobCancel(fmt.Errorf("function trigger %s: %w", a.TriggerID, err))
	}
	if err != nil {
		return fmt.Errorf("function trigger %s: %w", a.TriggerID, err)
	}
	return nil
}
