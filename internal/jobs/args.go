// Package jobs defines shared River job argument types used by both the
// tenant service (enqueuing) and workers (processing).
package jobs

import "github.com/riverqueue/river"

// ProvisionProjectArgs are the arguments for the async project provisioning job.
type ProvisionProjectArgs struct {
	ProjectID string `json:"project_id"`
	Slug      string `json:"slug"`
	Plan      string `json:"plan"`
}

// Kind returns the unique job type identifier for River.
func (ProvisionProjectArgs) Kind() string { return "provision_project" }

// InsertOpts returns default insert options including max retry attempts.
func (ProvisionProjectArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
	}
}

// TenantExportArgs are the arguments for an async full-tenant DSAR export.
type TenantExportArgs struct {
	ExportID  string `json:"export_id"`
	ProjectID string `json:"project_id"`
	Format    string `json:"format"`
}

func (TenantExportArgs) Kind() string { return "export_tenant" }
func (TenantExportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 2}
}

// UserExportArgs are the arguments for an async per-user DSAR export.
type UserExportArgs struct {
	ExportID  string `json:"export_id"`
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	Format    string `json:"format"`
}

func (UserExportArgs) Kind() string { return "export_user" }
func (UserExportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 2}
}

// SendDripEmailArgs is one step of the 6-mail onboarding drip series.
// Enqueued by tenant/auth signup with ScheduledAt = signupTime +
// step*OnboardingIntervalDays. See the SendDripEmailWorker for what
// it does at execution time (opt-out check, idempotency guard,
// render, send, audit-log row).
//
// Phase C of the public-beta launch plan.
type SendDripEmailArgs struct {
	UserID string `json:"user_id"`
	Step   int    `json:"step"` // 0..5 (six-mail drip)
}

func (SendDripEmailArgs) Kind() string { return "send_drip_email" }

// MaxAttempts = 3: transient TEM failures should retry, but a
// permanent bounce or template render error shouldn't spam the
// user's inbox with the same mail on 25 retries. Failed sends land
// in drip_email_sends with status='failed' + error message so we
// can inspect without needing to grep the worker logs.
func (SendDripEmailArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3}
}
