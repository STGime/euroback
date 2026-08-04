package plans

import (
	"context"
	"fmt"
	"log/slog"
)

// CheckProjectLimit verifies the owner has not exceeded their
// project limit. Elevation is based on the highest-tier project the
// owner already has PLUS the closed-beta flags on their platform_user
// row:
//
//   * team_beta_access or legal_team_beta_access → team's ProjectLimit
//     applies (currently 50). This handles the "beta-granted user with
//     2 Free projects" case that would otherwise hit Free's limit=2
//     before ever getting to create their first Team project.
//   * else any existing team/legal_team project → team's ProjectLimit
//   * else any existing pro project → pro's ProjectLimit (10)
//   * else free's ProjectLimit (2)
//
// The check runs BEFORE the request body is decoded (handler.go:137),
// so we can't plumb the requested plan through — the beta-flag check
// on platform_users is what carries the intent instead. Fix from M2
// review bug_012.
func (s *LimitsService) CheckProjectLimit(ctx context.Context, ownerID string) error {
	var totalCount, teamCount, proCount int
	var hasTeamBeta, hasLegalTeamBeta bool
	err := s.pool.QueryRow(ctx,
		`SELECT
			count(p.*),
			count(p.*) FILTER (WHERE p.plan IN ('team','legal_team')),
			count(p.*) FILTER (WHERE p.plan = 'pro'),
			COALESCE(bool_or(u.team_beta_access), false),
			COALESCE(bool_or(u.legal_team_beta_access), false)
		 FROM public.platform_users u
		 LEFT JOIN public.projects p
		        ON p.owner_id = u.id AND p.status = 'active'
		 WHERE u.id = $1::uuid`,
		ownerID,
	).Scan(&totalCount, &teamCount, &proCount, &hasTeamBeta, &hasLegalTeamBeta)
	if err != nil {
		slog.Error("check project limit: count failed", "owner_id", ownerID, "error", err)
		return fmt.Errorf("failed to count projects: %w", err)
	}

	effectivePlan := "free"
	switch {
	case hasTeamBeta || hasLegalTeamBeta || teamCount > 0:
		effectivePlan = "team"
	case proCount > 0:
		effectivePlan = "pro"
	}

	limits, err := s.GetLimits(ctx, effectivePlan)
	if err != nil {
		return err
	}

	if totalCount >= limits.ProjectLimit {
		slog.Warn("project limit reached", "owner_id", ownerID, "plan", effectivePlan, "current", totalCount, "limit", limits.ProjectLimit)
		switch effectivePlan {
		case "free":
			return fmt.Errorf("free plan limited to %d projects — upgrade a project to Pro to create up to %d", limits.ProjectLimit, 10)
		case "pro":
			return fmt.Errorf("pro plan limited to %d projects", limits.ProjectLimit)
		default:
			return fmt.Errorf("%s plan limited to %d projects", effectivePlan, limits.ProjectLimit)
		}
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
// Uses GetEffectiveProjectLimits so grandfathered Free projects keep
// the pre-Phase-B 10 000 cap until their `grandfathered_until` window
// closes (public-beta launch plan decision #3, migration 000076).
func (s *LimitsService) CheckMAULimit(ctx context.Context, projectID, schemaName string) error {
	limits, err := s.GetEffectiveProjectLimits(ctx, projectID)
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

// CheckCustomDomain gates the CNAME-your-own-domain feature (Phase B
// binary Pro-only gate, migration 000075). Free = false, Pro = true.
// Doesn't consider grandfathering — the feature didn't exist on the
// pre-Phase-B plan, so enabling it for grandfathered Free projects
// would be a real product change, not a grandfather.
func (s *LimitsService) CheckCustomDomain(ctx context.Context, projectID string) error {
	limits, err := s.GetProjectLimits(ctx, projectID)
	if err != nil {
		return err
	}
	if !limits.CustomDomain {
		slog.Warn("custom domain not available", "project_id", projectID, "plan", limits.Plan)
		return fmt.Errorf("custom domains are not available on the %s plan, upgrade to pro", limits.Plan)
	}
	return nil
}

// CheckBYOSMTP gates bring-your-own-SMTP for auth mail (Phase B
// binary Pro-only gate, migration 000075).
func (s *LimitsService) CheckBYOSMTP(ctx context.Context, projectID string) error {
	limits, err := s.GetProjectLimits(ctx, projectID)
	if err != nil {
		return err
	}
	if !limits.BYOSMTP {
		slog.Warn("BYO SMTP not available", "project_id", projectID, "plan", limits.Plan)
		return fmt.Errorf("BYO SMTP is not available on the %s plan, upgrade to pro", limits.Plan)
	}
	return nil
}

// CheckQuotaAlerts gates Slack / webhook alerts at 80% of any quota
// (Phase B binary Pro-only gate, migration 000075).
func (s *LimitsService) CheckQuotaAlerts(ctx context.Context, projectID string) error {
	limits, err := s.GetProjectLimits(ctx, projectID)
	if err != nil {
		return err
	}
	if !limits.QuotaAlerts {
		slog.Warn("quota alerts not available", "project_id", projectID, "plan", limits.Plan)
		return fmt.Errorf("quota alerts are not available on the %s plan, upgrade to pro", limits.Plan)
	}
	return nil
}

// CheckDedicatedDB gates features that only Team-tier projects have —
// backup / PITR / restore endpoints, direct DATABASE_URL exposure (M4),
// and the M2b Legal-Team compliance surface. Returns nil for any plan
// with `dedicated_db = true` in plan_limits (currently Team; Legal-Team
// will inherit true when M2b lands).
//
// Callers should surface this as HTTP 402 (payment required) so the
// console can render an upgrade prompt.
func (s *LimitsService) CheckDedicatedDB(ctx context.Context, projectID string) error {
	limits, err := s.GetProjectLimits(ctx, projectID)
	if err != nil {
		return err
	}
	if !limits.DedicatedDB {
		slog.Warn("dedicated-DB feature not available", "project_id", projectID, "plan", limits.Plan)
		return fmt.Errorf("this feature requires a dedicated database — upgrade to Team from %s", limits.Plan)
	}
	return nil
}
