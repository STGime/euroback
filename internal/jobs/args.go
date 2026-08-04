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

// ProvisionTeamDatabaseArgs is enqueued when a Team-tier project is
// created. The worker (internal/workers/provision_team_db.go) calls
// the dbprovider Provision method, polls Health until StateActive,
// then flips the project_databases row's state.
//
// M1 of the Team-tier plan (milestone #2). See
// ~/.claude/plans/zazzy-booping-fountain.md.
type ProvisionTeamDatabaseArgs struct {
	ProjectID string `json:"project_id"`
	Slug      string `json:"slug"`
	Provider  string `json:"provider"` // "scaleway" today; EU-only allow-list
	Region    string `json:"region"`   // "fr-par" today
	Size      string `json:"size"`     // "small" | "medium" | "large"
}

func (ProvisionTeamDatabaseArgs) Kind() string { return "provision_team_database" }

// MaxAttempts = 5 with River's exponential backoff. Provider
// provisioning can take 2-5 minutes; a network flake during the
// polling loop shouldn't burn the whole job — we want a few
// automatic retries before we page ops.
func (ProvisionTeamDatabaseArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5}
}

// DeprovisionTeamDatabaseArgs is enqueued by the periodic sweep to
// tear down a dedicated instance after its 7-day rollback window
// has elapsed. Also usable one-off if an operator wants to force
// deletion after a Team-tier project is fully removed.
type DeprovisionTeamDatabaseArgs struct {
	ProjectDatabaseID string `json:"project_database_id"`
}

func (DeprovisionTeamDatabaseArgs) Kind() string { return "deprovision_team_database" }

// MaxAttempts = 5 — provider deletes are idempotent (404 is treated
// as success), so retries are safe.
func (DeprovisionTeamDatabaseArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5}
}

// RestoreTeamDatabaseArgs is enqueued when a user triggers a
// snapshot restore or a PITR restore (Team-tier M3). Carries just
// the restore_operations row ID — the worker reads every other
// input (source_ref, target_time, old_instance_id) from that row so
// the job payload stays tiny and idempotent-checkable.
type RestoreTeamDatabaseArgs struct {
	RestoreOperationID string `json:"restore_operation_id"`
}

func (RestoreTeamDatabaseArgs) Kind() string { return "restore_team_database" }

// MaxAttempts = 3 — provider restores are expensive; a persistent
// failure should surface fast to a human rather than burn Scaleway
// spend across 5 attempts. The worker itself is careful to update
// restore_operations.state='failed' on terminal errors.
func (RestoreTeamDatabaseArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3}
}
