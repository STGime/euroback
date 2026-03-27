package plans

import (
	"context"
	"fmt"
	"log/slog"
)

// CheckProjectLimit verifies the owner has not exceeded their project limit.
// Logic: if the owner has any pro project, they get the pro project limit (10).
// Otherwise they get the free limit (2).
func (s *LimitsService) CheckProjectLimit(ctx context.Context, ownerID string) error {
	var totalCount, proCount int
	err := s.pool.QueryRow(ctx,
		`SELECT
			count(*),
			count(*) FILTER (WHERE plan = 'pro')
		 FROM projects
		 WHERE owner_id = $1::uuid AND status = 'active'`,
		ownerID,
	).Scan(&totalCount, &proCount)
	if err != nil {
		slog.Error("check project limit: count failed", "owner_id", ownerID, "error", err)
		return fmt.Errorf("failed to count projects: %w", err)
	}

	// Determine effective plan based on whether user has any pro projects.
	effectivePlan := "free"
	if proCount > 0 {
		effectivePlan = "pro"
	}

	limits, err := s.GetLimits(ctx, effectivePlan)
	if err != nil {
		return err
	}

	if totalCount >= limits.ProjectLimit {
		if effectivePlan == "free" {
			slog.Warn("project limit reached", "owner_id", ownerID, "plan", effectivePlan, "current", totalCount, "limit", limits.ProjectLimit)
			return fmt.Errorf("free plan limited to %d projects — upgrade a project to Pro to create up to %d", limits.ProjectLimit, 10)
		}
		slog.Warn("project limit reached", "owner_id", ownerID, "plan", effectivePlan, "current", totalCount, "limit", limits.ProjectLimit)
		return fmt.Errorf("pro plan limited to %d projects", limits.ProjectLimit)
	}

	return nil
}

// CheckWebhookLimit verifies the project has not exceeded its plan's webhook limit.
// A webhook_limit of 0 means unlimited.
func (s *LimitsService) CheckWebhookLimit(ctx context.Context, projectID string) error {
	limits, err := s.GetProjectLimits(ctx, projectID)
	if err != nil {
		return err
	}

	// 0 = unlimited
	if limits.WebhookLimit == 0 {
		return nil
	}

	var count int
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM webhooks WHERE project_id = $1`, projectID,
	).Scan(&count)
	if err != nil {
		slog.Error("check webhook limit: count failed", "project_id", projectID, "error", err)
		return fmt.Errorf("failed to count webhooks: %w", err)
	}

	if count >= limits.WebhookLimit {
		slog.Warn("webhook limit reached", "project_id", projectID, "plan", limits.Plan, "current", count, "limit", limits.WebhookLimit)
		return fmt.Errorf("%s plan limited to %d webhooks, upgrade to pro", limits.Plan, limits.WebhookLimit)
	}

	return nil
}

// CheckMAULimit verifies the project has not exceeded its plan's monthly active user limit.
func (s *LimitsService) CheckMAULimit(ctx context.Context, projectID, schemaName string) error {
	limits, err := s.GetProjectLimits(ctx, projectID)
	if err != nil {
		return err
	}

	var count int
	query := fmt.Sprintf(`SELECT count(*) FROM %q.users`, schemaName)
	err = s.pool.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		slog.Error("check MAU limit: count failed", "project_id", projectID, "schema", schemaName, "error", err)
		return fmt.Errorf("failed to count users: %w", err)
	}

	if count >= limits.MAULimit {
		slog.Warn("MAU limit reached", "project_id", projectID, "plan", limits.Plan, "current", count, "limit", limits.MAULimit)
		return fmt.Errorf("%s plan limited to %d monthly active users, upgrade to pro", limits.Plan, limits.MAULimit)
	}

	return nil
}

// CheckCustomTemplates verifies the project's plan allows custom email templates.
func (s *LimitsService) CheckCustomTemplates(ctx context.Context, projectID string) error {
	limits, err := s.GetProjectLimits(ctx, projectID)
	if err != nil {
		return err
	}

	if !limits.CustomTemplates {
		slog.Warn("custom templates not available", "project_id", projectID, "plan", limits.Plan)
		return fmt.Errorf("custom email templates are not available on the %s plan, upgrade to pro", limits.Plan)
	}

	return nil
}

// GetUploadSizeLimit returns the maximum upload size in bytes for the project's plan.
func (s *LimitsService) GetUploadSizeLimit(ctx context.Context, projectID string) (int64, error) {
	limits, err := s.GetProjectLimits(ctx, projectID)
	if err != nil {
		return 0, err
	}

	return int64(limits.UploadSizeMB) * 1024 * 1024, nil
}
