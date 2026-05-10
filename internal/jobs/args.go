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
