package plans

import (
	"context"
	"fmt"
	"log/slog"
)

// ProjectUsage holds the current resource usage for a project.
type ProjectUsage struct {
	DatabaseSizeMB float64 `json:"database_size_mb"`
	StorageSizeMB  float64 `json:"storage_size_mb"`
	MAUCount       int     `json:"mau_count"`
	WebhookCount   int     `json:"webhook_count"`
	ProjectCount   int     `json:"project_count"`
}

// GetUsage queries the current resource usage for a project.
func (s *LimitsService) GetUsage(ctx context.Context, projectID, schemaName string) (*ProjectUsage, error) {
	usage := &ProjectUsage{}

	// Database size: sum of all table sizes in the tenant schema.
	var dbSizeBytes int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(pg_total_relation_size(schemaname || '.' || tablename)), 0)
		 FROM pg_tables WHERE schemaname = $1`, schemaName,
	).Scan(&dbSizeBytes)
	if err != nil {
		slog.Error("get usage: db size query failed", "project_id", projectID, "schema", schemaName, "error", err)
		return nil, fmt.Errorf("failed to query database size: %w", err)
	}
	usage.DatabaseSizeMB = float64(dbSizeBytes) / (1024 * 1024)

	// MAU count: number of users in the tenant schema.
	query := fmt.Sprintf(`SELECT count(*) FROM %q.users`, schemaName)
	err = s.pool.QueryRow(ctx, query).Scan(&usage.MAUCount)
	if err != nil {
		slog.Error("get usage: MAU count failed", "project_id", projectID, "schema", schemaName, "error", err)
		return nil, fmt.Errorf("failed to count users: %w", err)
	}

	// Webhook count.
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM webhooks WHERE project_id = $1`, projectID,
	).Scan(&usage.WebhookCount)
	if err != nil {
		slog.Error("get usage: webhook count failed", "project_id", projectID, "error", err)
		return nil, fmt.Errorf("failed to count webhooks: %w", err)
	}

	// Project count for the owner.
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM projects WHERE owner_id = (SELECT owner_id FROM projects WHERE id = $1) AND status = 'active'`,
		projectID,
	).Scan(&usage.ProjectCount)
	if err != nil {
		slog.Error("get usage: project count failed", "project_id", projectID, "error", err)
		return nil, fmt.Errorf("failed to count projects: %w", err)
	}

	return usage, nil
}
