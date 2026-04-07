package functions

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FunctionTrigger links an edge function to database table events.
type FunctionTrigger struct {
	ID         string   `json:"id"`
	FunctionID string   `json:"function_id"`
	ProjectID  string   `json:"project_id"`
	TableName  string   `json:"table_name"`
	Events     []string `json:"events"`
	Enabled    bool     `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
}

// TriggerService provides CRUD for function_triggers.
type TriggerService struct {
	pool *pgxpool.Pool
}

// NewTriggerService creates a new trigger service.
func NewTriggerService(pool *pgxpool.Pool) *TriggerService {
	return &TriggerService{pool: pool}
}

// CreateTriggerRequest is the payload for creating a function trigger.
type CreateTriggerRequest struct {
	TableName string   `json:"table_name"`
	Events    []string `json:"events"`
}

// Create creates a new function trigger.
func (ts *TriggerService) Create(ctx context.Context, projectID, functionID string, req CreateTriggerRequest) (*FunctionTrigger, error) {
	if req.TableName == "" {
		return nil, fmt.Errorf("table_name is required")
	}
	if len(req.Events) == 0 {
		return nil, fmt.Errorf("at least one event is required")
	}

	// Validate events.
	validEvents := map[string]bool{"INSERT": true, "UPDATE": true, "DELETE": true}
	for _, e := range req.Events {
		if !validEvents[e] {
			return nil, fmt.Errorf("invalid event %q: must be INSERT, UPDATE, or DELETE", e)
		}
	}

	var t FunctionTrigger
	err := ts.pool.QueryRow(ctx,
		`INSERT INTO function_triggers (function_id, project_id, table_name, events)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, function_id, project_id, table_name, events, enabled, created_at`,
		functionID, projectID, req.TableName, req.Events,
	).Scan(&t.ID, &t.FunctionID, &t.ProjectID, &t.TableName, &t.Events, &t.Enabled, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create function trigger: %w", err)
	}
	return &t, nil
}

// List returns all triggers for a function.
func (ts *TriggerService) List(ctx context.Context, projectID, functionID string) ([]FunctionTrigger, error) {
	rows, err := ts.pool.Query(ctx,
		`SELECT id, function_id, project_id, table_name, events, enabled, created_at
		 FROM function_triggers
		 WHERE project_id = $1 AND function_id = $2
		 ORDER BY created_at`, projectID, functionID)
	if err != nil {
		return nil, fmt.Errorf("list function triggers: %w", err)
	}
	defer rows.Close()

	var triggers []FunctionTrigger
	for rows.Next() {
		var t FunctionTrigger
		if err := rows.Scan(&t.ID, &t.FunctionID, &t.ProjectID, &t.TableName, &t.Events, &t.Enabled, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan function trigger: %w", err)
		}
		triggers = append(triggers, t)
	}
	if triggers == nil {
		triggers = []FunctionTrigger{}
	}
	return triggers, nil
}

// Delete removes a function trigger.
func (ts *TriggerService) Delete(ctx context.Context, projectID, triggerID string) error {
	tag, err := ts.pool.Exec(ctx,
		`DELETE FROM function_triggers WHERE project_id = $1 AND id = $2`,
		projectID, triggerID)
	if err != nil {
		return fmt.Errorf("delete function trigger: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("trigger not found")
	}
	return nil
}
