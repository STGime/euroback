package plans

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// ProjectUsage holds the current resource usage for a project.
type ProjectUsage struct {
	DatabaseSizeMB    float64 `json:"database_size_mb"`
	StorageSizeMB     float64 `json:"storage_size_mb"`
	MAUCount          int     `json:"mau_count"`
	WebhookCount      int     `json:"webhook_count"`
	ProjectCount      int     `json:"project_count"`
	EdgeFunctionCount int     `json:"edge_function_count"`
}

// GetUsage queries the current resource usage for a project.
func (s *LimitsService) GetUsage(ctx context.Context, projectID, schemaName string) (*ProjectUsage, error) {
	usage := &ProjectUsage{}

	// Database size: sum of all table sizes in the tenant schema.
	var dbSizeBytes int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(pg_total_relation_size(quote_ident(schemaname) || '.' || quote_ident(tablename))), 0)
		 FROM pg_tables WHERE schemaname = $1`, schemaName,
	).Scan(&dbSizeBytes)
	if err != nil {
		slog.Error("get usage: db size query failed", "project_id", projectID, "schema", schemaName, "error", err)
		// Non-fatal: return 0 if the query fails (e.g. permission issue)
		dbSizeBytes = 0
	}
	usage.DatabaseSizeMB = float64(dbSizeBytes) / (1024 * 1024)

	// Storage size: sum of uploaded file sizes tracked in the tenant
	// storage_objects table. Non-fatal on error — storage is platform-managed
	// and may not be populated yet for a new project.
	escSchema := strings.ReplaceAll(schemaName, `"`, `""`)
	var storageBytes int64
	storageQuery := fmt.Sprintf(`SELECT COALESCE(SUM(size_bytes), 0) FROM "%s".storage_objects`, escSchema)
	if err := s.pool.QueryRow(ctx, storageQuery).Scan(&storageBytes); err != nil {
		slog.Error("get usage: storage size query failed", "project_id", projectID, "schema", schemaName, "error", err)
		storageBytes = 0
	}
	usage.StorageSizeMB = float64(storageBytes) / (1024 * 1024)

	// MAU count: number of users in the tenant schema.
	mauQuery := fmt.Sprintf(`SELECT count(*) FROM "%s".users`, escSchema)
	err = s.pool.QueryRow(ctx, mauQuery).Scan(&usage.MAUCount)
	if err != nil {
		slog.Error("get usage: MAU count failed", "project_id", projectID, "schema", schemaName, "error", err)
		usage.MAUCount = 0
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

	// Edge function count.
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM edge_functions WHERE project_id = $1`, projectID,
	).Scan(&usage.EdgeFunctionCount)
	if err != nil {
		slog.Error("get usage: edge function count failed", "project_id", projectID, "error", err)
		usage.EdgeFunctionCount = 0
	}

	return usage, nil
}
