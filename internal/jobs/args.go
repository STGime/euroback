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
