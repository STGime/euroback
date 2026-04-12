package plans

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// EmailSender is the minimal interface the alerts job needs from the email
// service. Declared locally to avoid an import cycle
// (internal/email already imports internal/plans).
type EmailSender interface {
	Configured() bool
	SendRaw(ctx context.Context, to, subject, htmlBody string) error
}

// usageAlertInterval controls how often the alerts job runs.
const usageAlertInterval = 24 * time.Hour

// thresholds are the percentage breakpoints at which we notify a project
// owner. Each (project, dimension, threshold) pair is only emailed once per
// breach: the sent marker in public.usage_alerts_sent is cleared when usage
// drops back below the threshold, re-arming the alert for the next breach.
var thresholds = []int{80, 90, 100}

// dimension represents one measurable usage axis. The "limit" closure
// resolves the plan's cap for that dimension; "value" resolves the current
// usage. A limit of 0 means "unlimited" and is skipped.
type dimension struct {
	key      string // matches the CHECK constraint on usage_alerts_sent.dimension
	label    string // human-readable label for the email body
	unit     string // "MB", "users", "functions"
	current  func(u *ProjectUsage) float64
	capacity func(l *PlanLimits) float64
}

var dimensions = []dimension{
	{
		key:      "db_size",
		label:    "Database size",
		unit:     "MB",
		current:  func(u *ProjectUsage) float64 { return u.DatabaseSizeMB },
		capacity: func(l *PlanLimits) float64 { return float64(l.DBSizeMB) },
	},
	{
		key:      "storage",
		label:    "Storage",
		unit:     "MB",
		current:  func(u *ProjectUsage) float64 { return u.StorageSizeMB },
		capacity: func(l *PlanLimits) float64 { return float64(l.StorageMB) },
	},
	{
		key:      "mau",
		label:    "Monthly active users",
		unit:     "users",
		current:  func(u *ProjectUsage) float64 { return float64(u.MAUCount) },
		capacity: func(l *PlanLimits) float64 { return float64(l.MAULimit) },
	},
	{
		key:      "edge_functions",
		label:    "Edge functions",
		unit:     "functions",
		current:  func(u *ProjectUsage) float64 { return float64(u.EdgeFunctionCount) },
		capacity: func(l *PlanLimits) float64 { return float64(l.EdgeFunctionLimit) },
	},
	{
		key:      "webhooks",
		label:    "Webhooks",
		unit:     "webhooks",
		current:  func(u *ProjectUsage) float64 { return float64(u.WebhookCount) },
		capacity: func(l *PlanLimits) float64 { return float64(l.WebhookLimit) },
	},
}

// StartUsageAlerts starts a background goroutine that scans all active
// projects once per day, compares usage against plan_limits, and sends an
// email to the project owner when any dimension crosses the 80/90/100%
// thresholds. If emailService is nil or not configured, the loop still runs
// (and logs breaches) but does not send mail — this keeps local dev usable
// without a TEM key while still surfacing problems in logs.
func (s *LimitsService) StartUsageAlerts(ctx context.Context, emailService EmailSender) {
	go func() {
		ticker := time.NewTicker(usageAlertInterval)
		defer ticker.Stop()

		// Run once on startup.
		s.scanAndAlert(ctx, emailService)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.scanAndAlert(ctx, emailService)
			}
		}
	}()
}

// ScanAndAlert walks every active project, computes usage, and fires/clears
// alerts. Exported so an admin/debug endpoint (or test harness) can trigger
// an immediate scan without waiting for the 24h tick.
func (s *LimitsService) ScanAndAlert(ctx context.Context, emailService EmailSender) {
	s.scanAndAlert(ctx, emailService)
}

// scanAndAlert is the unexported implementation used by both the goroutine
// loop and the exported ScanAndAlert trigger.
func (s *LimitsService) scanAndAlert(ctx context.Context, emailService EmailSender) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.name, p.schema_name, p.plan, u.email
		 FROM public.projects p
		 JOIN public.platform_users u ON u.id = p.owner_id
		 WHERE p.status = 'active' AND p.schema_name IS NOT NULL`,
	)
	if err != nil {
		slog.Error("usage alerts: failed to list projects", "error", err)
		return
	}
	defer rows.Close()

	type projectRow struct {
		id         string
		name       string
		schemaName string
		plan       string
		ownerEmail string
	}
	var projects []projectRow
	for rows.Next() {
		var p projectRow
		if err := rows.Scan(&p.id, &p.name, &p.schemaName, &p.plan, &p.ownerEmail); err != nil {
			slog.Error("usage alerts: scan row failed", "error", err)
			continue
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		slog.Error("usage alerts: iterate rows failed", "error", err)
		return
	}

	firedCount := 0
	clearedCount := 0
	for _, p := range projects {
		usage, err := s.GetUsage(ctx, p.id, p.schemaName)
		if err != nil {
			slog.Error("usage alerts: GetUsage failed", "project_id", p.id, "error", err)
			continue
		}
		limits, err := s.GetLimits(ctx, p.plan)
		if err != nil {
			slog.Error("usage alerts: GetLimits failed", "project_id", p.id, "plan", p.plan, "error", err)
			continue
		}

		for _, dim := range dimensions {
			cap := dim.capacity(limits)
			if cap <= 0 {
				// 0 means unlimited on this plan — nothing to alert on.
				// Also clear any stale markers from when the user was on a
				// lower plan.
				s.clearAllThresholds(ctx, p.id, dim.key)
				continue
			}
			current := dim.current(usage)
			percent := (current / cap) * 100

			for _, threshold := range thresholds {
				crossed := percent >= float64(threshold)
				already := s.alertAlreadySent(ctx, p.id, dim.key, threshold)

				switch {
				case crossed && !already:
					// Fire a new alert.
					if err := s.fireAlert(ctx, emailService, p.id, p.name, p.ownerEmail, dim, threshold, current, cap, percent); err != nil {
						slog.Error("usage alerts: fire failed",
							"project_id", p.id, "dimension", dim.key, "threshold", threshold, "error", err)
						continue
					}
					firedCount++
				case !crossed && already:
					// Usage dropped back below the threshold — re-arm.
					if err := s.clearAlertMarker(ctx, p.id, dim.key, threshold); err != nil {
						slog.Error("usage alerts: clear failed",
							"project_id", p.id, "dimension", dim.key, "threshold", threshold, "error", err)
						continue
					}
					clearedCount++
				}
			}
		}
	}

	if firedCount > 0 || clearedCount > 0 {
		slog.Info("usage alerts: scan complete",
			"projects", len(projects),
			"fired", firedCount,
			"cleared", clearedCount,
		)
	}
}

// alertAlreadySent reports whether the (project, dimension, threshold)
// marker already exists in public.usage_alerts_sent.
func (s *LimitsService) alertAlreadySent(ctx context.Context, projectID, dim string, threshold int) bool {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM public.usage_alerts_sent
			WHERE project_id = $1 AND dimension = $2 AND threshold = $3
		 )`,
		projectID, dim, threshold,
	).Scan(&exists)
	if err != nil {
		slog.Error("usage alerts: check sent marker failed",
			"project_id", projectID, "dimension", dim, "threshold", threshold, "error", err)
		return false
	}
	return exists
}

// fireAlert inserts a sent marker and sends the email. If the email fails,
// the marker is rolled back so the alert will retry on the next scan.
func (s *LimitsService) fireAlert(
	ctx context.Context,
	emailService EmailSender,
	projectID, projectName, ownerEmail string,
	dim dimension,
	threshold int,
	current, cap, percent float64,
) error {
	// Insert marker first (idempotent via ON CONFLICT).
	_, err := s.pool.Exec(ctx,
		`INSERT INTO public.usage_alerts_sent (project_id, dimension, threshold)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (project_id, dimension, threshold) DO UPDATE SET sent_at = now()`,
		projectID, dim.key, threshold,
	)
	if err != nil {
		return fmt.Errorf("insert marker: %w", err)
	}

	slog.Warn("usage alert fired",
		"project_id", projectID,
		"project_name", projectName,
		"dimension", dim.key,
		"threshold", threshold,
		"current", current,
		"cap", cap,
		"percent", math.Round(percent*10)/10,
	)

	if emailService == nil || !emailService.Configured() {
		// No email available — log-only alert is enough (marker persists).
		return nil
	}

	subject, body := renderAlertEmail(projectName, dim, threshold, current, cap, percent)
	if err := emailService.SendRaw(ctx, ownerEmail, subject, body); err != nil {
		// Roll back marker so next run retries.
		_, _ = s.pool.Exec(ctx,
			`DELETE FROM public.usage_alerts_sent
			 WHERE project_id = $1 AND dimension = $2 AND threshold = $3`,
			projectID, dim.key, threshold,
		)
		return fmt.Errorf("send email: %w", err)
	}
	return nil
}

// clearAlertMarker deletes a single (project, dimension, threshold) row.
// Called when usage drops back below the threshold.
func (s *LimitsService) clearAlertMarker(ctx context.Context, projectID, dim string, threshold int) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM public.usage_alerts_sent
		 WHERE project_id = $1 AND dimension = $2 AND threshold = $3`,
		projectID, dim, threshold,
	)
	return err
}

// clearAllThresholds deletes every marker for a dimension (used when the
// plan switched to "unlimited" for that dimension).
func (s *LimitsService) clearAllThresholds(ctx context.Context, projectID, dim string) {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM public.usage_alerts_sent WHERE project_id = $1 AND dimension = $2`,
		projectID, dim,
	)
	if err != nil {
		slog.Error("usage alerts: clear all failed",
			"project_id", projectID, "dimension", dim, "error", err)
	}
}

// renderAlertEmail builds the subject + HTML body for a usage alert.
//
// The subject describes which warning threshold was crossed so that the
// three alerts for a single breach (80/90/100%) are distinguishable. The
// body lead paragraph shows the *actual* current percent, which can be far
// higher than the threshold that triggered this particular email — e.g. a
// project at 200% of its plan fires all three thresholds and every email
// correctly reports "at 200% of your plan limit".
//
// Kept as a plain Go string template — no per-project customization because
// this is a platform-level notification, not a user-facing email.
func renderAlertEmail(projectName string, dim dimension, threshold int, current, cap, percent float64) (string, string) {
	var subject string
	var headline string
	switch threshold {
	case 100:
		subject = fmt.Sprintf("[Eurobase] %s: %s limit reached", projectName, dim.label)
		headline = "Plan limit reached"
	case 90:
		subject = fmt.Sprintf("[Eurobase] %s: %s crossed 90%% threshold", projectName, dim.label)
		headline = "90% warning threshold crossed"
	default: // 80
		subject = fmt.Sprintf("[Eurobase] %s: %s crossed 80%% threshold", projectName, dim.label)
		headline = "80% warning threshold crossed"
	}

	// The lead line shows actual usage — never the threshold integer — so
	// the email is honest even when a single scan crosses multiple
	// thresholds at once.
	lead := fmt.Sprintf("Your %s usage is at <strong>%.0f%%</strong> of your plan limit on project <strong>%s</strong>.",
		dim.label, percent, projectName)

	// Use whole numbers for countable items (users, functions, webhooks)
	// and one decimal for sizes (MB).
	currentStr := fmt.Sprintf("%.0f", current)
	capStr := fmt.Sprintf("%.0f", cap)
	if dim.unit == "MB" {
		currentStr = fmt.Sprintf("%.1f", current)
		capStr = fmt.Sprintf("%.1f", cap)
	}

	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; max-width: 600px; margin: 0 auto; padding: 24px; color: #111;">
  <h2 style="color: #1e40af; margin-top: 0;">Usage alert: %s</h2>
  <p>%s</p>
  <p style="color: #6b7280; font-size: 14px;">%s</p>
  <table style="width: 100%%; border-collapse: collapse; margin: 16px 0; border: 1px solid #e5e7eb; border-radius: 8px;">
    <tr style="background: #f9fafb;">
      <td style="padding: 12px 16px; border-bottom: 1px solid #e5e7eb;"><strong>Metric</strong></td>
      <td style="padding: 12px 16px; border-bottom: 1px solid #e5e7eb;">%s</td>
    </tr>
    <tr>
      <td style="padding: 12px 16px; border-bottom: 1px solid #e5e7eb;"><strong>Current usage</strong></td>
      <td style="padding: 12px 16px; border-bottom: 1px solid #e5e7eb;">%s %s</td>
    </tr>
    <tr style="background: #f9fafb;">
      <td style="padding: 12px 16px; border-bottom: 1px solid #e5e7eb;"><strong>Plan limit</strong></td>
      <td style="padding: 12px 16px; border-bottom: 1px solid #e5e7eb;">%s %s</td>
    </tr>
    <tr>
      <td style="padding: 12px 16px;"><strong>Percent used</strong></td>
      <td style="padding: 12px 16px;">%.0f%%</td>
    </tr>
  </table>
  <p>If you're on the free plan, consider upgrading to Pro for higher limits. You can manage your project and plan in the Eurobase console.</p>
  <p style="color: #6b7280; font-size: 12px; margin-top: 32px;">This is an automated alert from Eurobase. You'll receive one notification per threshold per breach — the alert will re-arm if usage drops below the threshold.</p>
</body>
</html>`,
		dim.label,
		lead,
		headline,
		dim.label,
		currentStr, dim.unit,
		capStr, dim.unit,
		percent,
	)

	return subject, body
}

