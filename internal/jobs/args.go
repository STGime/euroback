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
//
// UniqueOpts.ByArgs collapses duplicates keyed on (ProjectID, Slug,
// Provider, Region, Size). Two paths enqueue with those exact
// args: CreateProject on the create path, and HandleRetryProvisioning
// on the recovery path. Without dedup, a double-click / two admins
// / a client-side network retry all produce TWO Scaleway instances
// (~€50-500/mo each), and the retry handler's check-then-enqueue
// guard is a TOCTOU on the DB row. River's ByState defaults cover
// pending + available + running + retryable, so a NEW attempt is
// still allowed after a prior job COMPLETES or is CANCELLED —
// exactly the semantics the retry button needs.
func (ProvisionTeamDatabaseArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
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
//
// UniqueOpts.ByArgs collapses double-enqueues from the periodic
// sweeper (two overlapping ticks could otherwise queue the same
// project_database_id twice). River's ByState defaults cover
// pending + available + running + retryable — a completed or
// cancelled prior run doesn't block a fresh sweep, which matches
// the sweeper's "keep retrying until the row is hard-deleted"
// intent.
func (DeprovisionTeamDatabaseArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// BackfillRuntimeCredentialArgs is enqueued (one job per project)
// to run BootstrapDedicated against a Team-tier project that was
// provisioned BEFORE M2.5 part 2b landed and therefore has no
// non-owner runtime credential set. Without this, an existing Team
// project stays on the shared pool after TEAM_TIER_ROUTING flips
// (PoolCache.EffectiveCredential falls back to owner → RLS bypass →
// resolver returns nil to shared).
//
// The periodic sweeper (a separate scheduled worker) walks
// project_databases WHERE runtime_username IS NULL and fans out
// one of these per matching row.
type BackfillRuntimeCredentialArgs struct {
	ProjectDatabaseID string `json:"project_database_id"`
}

func (BackfillRuntimeCredentialArgs) Kind() string { return "backfill_runtime_credential" }

// MaxAttempts = 3 — bootstrap is idempotent (schema/role checks
// short-circuit on retry), so retries are safe, but a persistent
// failure is a real issue that should surface fast.
//
// UniqueOpts.ByArgs collapses duplicate pending jobs for the same
// project_database_id. Two enqueue sources exist that can target
// the same row: (a) the hourly sweeper, and (b) the M2.5 part 2b
// provision-worker resume path (`job.Attempt > 1`). Without dedup
// they can race — both calls to BootstrapDedicated rotate the
// runtime password (last ALTER ROLE wins on Scaleway) and the two
// SetRuntimeCredentials writes can persist a ciphertext that
// doesn't match the live password → silent auth failures on the
// dedicated instance. The DB-side WHERE guard on
// SetRuntimeCredentials narrows the race further; UniqueOpts is
// the primary defence.
func (BackfillRuntimeCredentialArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
		},
	}
}

// ReconcileBackupScheduleArgs is enqueued by StartBackupScheduleSweeper
// (one job per project_databases row still needing a Scaleway
// set-backup-schedule call). The worker looks up the project's plan
// to resolve retention days, calls Provider.SetBackupSchedule, and
// on success stamps project_databases.backup_schedule_applied_at.
//
// UniqueOpts.ByArgs collapses double-enqueues from overlapping
// sweep ticks — matches the DeprovisionTeamDatabaseArgs pattern in
// #455. MaxAttempts=5 covers Scaleway warmup rejections (a fresh
// instance may 5xx set-backup-schedule for a few minutes before
// the backup subsystem accepts writes; River's exponential backoff
// drains through the warmup window).
type ReconcileBackupScheduleArgs struct {
	ProjectDatabaseID string `json:"project_database_id"`
}

func (ReconcileBackupScheduleArgs) Kind() string { return "reconcile_backup_schedule" }
func (ReconcileBackupScheduleArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// DeliverAuditSyslogArgs is the syslog analogue of
// DeliverAuditWebhookArgs — enqueued per enabled syslog destination
// by the shared scheduler. Same idempotency / retry policy: unique
// by args (destination_id), MaxAttempts=5 with River's exponential
// backoff.
type DeliverAuditSyslogArgs struct {
	DestinationID string `json:"destination_id"`
}

func (DeliverAuditSyslogArgs) Kind() string { return "deliver_audit_syslog" }
func (DeliverAuditSyslogArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// DeliverAuditWebhookArgs is enqueued (per destination, periodically
// by the audit_webhook_scheduler sweeper — #354) to POST a batch of
// new audit_log rows to a tenant's registered webhook sink. Args
// carry only the destination row ID; the worker reads endpoint,
// secret_ref, format, enabled, last_cursor from the DB at execution
// time so a mutated destination row is picked up on the very next
// tick without job-payload staleness.
//
// UniqueOpts.ByArgs prevents a slow-sink destination from stacking
// up multiple in-flight jobs against the same row (the scheduler
// fires every 30s; a 60s-slow sink would otherwise queue two by the
// time the first finishes).
type DeliverAuditWebhookArgs struct {
	DestinationID string `json:"destination_id"`
}

func (DeliverAuditWebhookArgs) Kind() string { return "deliver_audit_webhook" }

// MaxAttempts = 5 with River's exponential backoff. A dead sink
// should retry a few times before giving up on this batch — River's
// per-attempt delay ramp keeps the worker from spinning against a
// broken sink. On terminal failure the last_cursor stays put so the
// next scheduled tick starts a fresh attempt.
func (DeliverAuditWebhookArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
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
