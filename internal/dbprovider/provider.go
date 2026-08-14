// Package dbprovider abstracts over managed Postgres providers used
// for Team-tier (and Legal-Team-tier) projects. Every project on
// those tiers gets its own dedicated instance rather than a schema on
// the base cluster (see CLAUDE.md for the shared-cluster model that
// Free + Pro tiers use).
//
// Scaleway managed PG (fr-par) is the initial implementation. The
// registry allows for other EU-headquartered providers (Aiven FI,
// OVHcloud FR, CleverCloud FR, self-hosted) as follow-ups. US-owned
// providers are refused at registration time — Eurobase's value
// proposition is EU sovereignty; a US provider (Neon, RDS,
// Supabase-hosted, GCP Cloud SQL) would break the story customers
// are paying for.
//
// M1 of the Team-tier plan (docs: milestone #2 on GitHub, plan at
// ~/.claude/plans/zazzy-booping-fountain.md).
package dbprovider

import (
	"context"
	"time"
)

// State is the lifecycle state of a managed-PG instance as reported
// by the underlying provider. Callers map it to
// project_databases.state.
type State string

const (
	StateProvisioning State = "provisioning"
	StateActive       State = "active"
	StateRestoring    State = "restoring"
	StateFailed       State = "failed"
	StateDeleting     State = "deleting"
	// StateUnknown is returned when the provider reports a state we
	// don't recognise. Callers should treat it as a transient warning
	// and retry Health() with backoff rather than as terminal.
	StateUnknown State = "unknown"
)

// Instance describes a live managed-PG instance owned by a Team-tier
// project. The provider is the only source of truth for ProviderID
// and lifecycle state; the rest is bookkeeping we mirror into
// project_databases.
//
// Password is returned in plaintext exactly once — at Provision time,
// where the caller immediately seals it before writing the row. Every
// subsequent read of an Instance leaves the field empty.
type Instance struct {
	ProviderID string
	Host       string
	Port       int
	DBName     string
	Username   string
	Password   string
	State      State
	Region     string
	CreatedAt  time.Time
}

// Snapshot is a point-in-time backup on the provider side. Retention
// is provider-defined by default (Scaleway RDB does daily backups
// with 7-day retention on the default plan); on-demand snapshots
// carry an ExpiresAt that the caller sets from
// plan_limits.backup_retention_days.
type Snapshot struct {
	ProviderID string
	InstanceID string
	Name       string
	SizeMB     int64
	CreatedAt  time.Time
	ExpiresAt  time.Time
	Kind       SnapshotKind
}

// SnapshotKind distinguishes scheduled provider backups from
// user-triggered on-demand backups. Used by the UI in M3 to badge
// the two differently.
type SnapshotKind string

const (
	SnapshotKindScheduled SnapshotKind = "scheduled"
	SnapshotKindOnDemand  SnapshotKind = "ondemand"
)

// ProvisionOpts is the caller-supplied set of provisioning inputs.
// SKU / node size / storage are provider-scoped — the Provider
// implementation picks a sensible default from the Size hint.
//
// M1 is single-region (fr-par); region is baked into the Provider
// implementation at construction time (Scaleway.defaultRegion),
// not passed per-call. Multi-region will need to thread region
// through EVERY method on the interface — not just Provision — so
// a plain `Region` field here would be misleading.
type ProvisionOpts struct {
	// ProjectID is the Eurobase project UUID; used to name the
	// instance so it's identifiable in the provider's dashboard.
	ProjectID string
	// Slug is the project slug — human-readable component of the
	// provider-side instance name.
	Slug string
	// Size is a coarse hint: SizeSmall (dev), SizeMedium (default),
	// SizeLarge. Providers map this to their own SKU catalogue.
	Size Size
	// IdempotencyKey, when non-empty, is sent to the provider so a
	// retry (River backoff, transient DB blip) lands on the same
	// instance rather than spinning up a duplicate billed resource.
	// Callers should use a deterministic value keyed on the job or
	// project — e.g. `provision-<job.ID>`. Empty is legal but not
	// recommended in production.
	IdempotencyKey string
}

// Size is a coarse hint mapped to provider SKUs at Provision time.
type Size string

const (
	SizeSmall  Size = "small"
	SizeMedium Size = "medium"
	SizeLarge  Size = "large"
)

// RestoreSource discriminates a snapshot restore from a PITR restore.
// Exactly one of SnapshotID or PITRTarget must be set — the Provider
// returns ErrInvalidRestoreSource otherwise.
type RestoreSource struct {
	SnapshotID string
	// PITRTarget is a wall-clock time inside the provider's PITR
	// window. Ignored if SnapshotID is set.
	PITRTarget time.Time
}

// Valid returns nil when the source correctly specifies exactly one
// restore vector, or ErrInvalidRestoreSource otherwise. Providers
// call this at the top of Restore.
func (s RestoreSource) Valid() error {
	hasSnapshot := s.SnapshotID != ""
	hasPITR := !s.PITRTarget.IsZero()
	if hasSnapshot == hasPITR {
		return ErrInvalidRestoreSource
	}
	return nil
}

// Provider is the interface every managed-PG backend implements.
// Six methods, in dependency order: provision, snapshot, list,
// restore, delete, health. All are context-aware; all return typed
// errors (see errors.go).
type Provider interface {
	// Name returns the stable identifier used in the registry and in
	// project_databases.provider (e.g. "scaleway"). Must be on the
	// EU-only allow-list (see registry.MustBeEU).
	Name() string

	// Provision creates a fresh instance. Returns once the provider
	// has assigned a resource ID; the instance may still be spinning
	// up (callers poll Health() to reach StateActive).
	Provision(ctx context.Context, opts ProvisionOpts) (*Instance, error)

	// Snapshot triggers an on-demand backup of an existing instance.
	// Returns the provider-assigned snapshot; snapshot itself may
	// still be running (call ListSnapshots to check state).
	Snapshot(ctx context.Context, instanceID string) (*Snapshot, error)

	// ListSnapshots enumerates every snapshot (scheduled +
	// on-demand) for an instance. Pagination is handled internally.
	ListSnapshots(ctx context.Context, instanceID string) ([]Snapshot, error)

	// Restore provisions a NEW instance from a snapshot ID or PITR
	// target time. Never mutates the source instance — always
	// two-instance. Callers manage the atomic cutover (see the M3
	// restore worker + project_databases.superseded_by).
	Restore(ctx context.Context, instanceID string, source RestoreSource) (*Instance, error)

	// Delete removes an instance. Idempotent — a 404 is not an
	// error. Provider-side may schedule delayed deletion (Scaleway
	// keeps deleted instances in cold storage for a short window);
	// the Instance is gone from the caller's view immediately.
	Delete(ctx context.Context, instanceID string) error

	// Health reports current lifecycle state without side effects.
	// Returns StateUnknown on unfamiliar provider states so callers
	// can distinguish transient confusion from terminal failure.
	Health(ctx context.Context, instanceID string) (State, error)

	// Describe returns the full Instance — state, endpoint,
	// username, region, creation time. Password is not returned
	// (never persisted plaintext beyond the Provision return).
	// Used by the provisioning worker to grab the current
	// host/port right at the state=active transition, since some
	// providers populate the endpoint only at that moment.
	Describe(ctx context.Context, instanceID string) (*Instance, error)
}

// PasswordRotator is an optional capability providers CAN implement
// to support direct-DATABASE_URL credential rotation (Team-tier M4).
// Kept as a distinct interface (rather than a required Provider
// method) so a future stub / self-hosted provider can omit it
// without breaking the base Provider contract.
//
// Callers should type-assert on this interface and return 501 if
// the provider doesn't implement it.
type PasswordRotator interface {
	// RotatePassword sets a new password for `username` on
	// `instanceID`. Implementations must invalidate old
	// credentials immediately on the provider side. Success
	// guarantees the new password is live — the caller then
	// re-seals and writes it to project_databases.
	RotatePassword(ctx context.Context, instanceID, username, newPassword string) error
}

// PrivilegeGranter is an optional capability providers CAN implement
// so the bootstrap flow can grant CONNECT + DB-level privileges via
// the provider's control-plane instead of via SQL. Needed by
// providers whose managed database is not owned by the customer-
// visible admin user — Scaleway RDB is one: the `rdb` database is
// owned by `_rdb_superadmin`, so `GRANT CONNECT` from an
// eurobase_owner session is a silent WARNING-not-error no-op. The
// provider's control-plane runs the grant as its superadmin,
// bypassing the ownership limitation.
//
// Providers where the customer admin genuinely owns the DB
// (self-hosted, vanilla docker PG) don't need this — SQL grants
// suffice — and can omit the interface.
//
// Permission values MUST be one of "all" | "readwrite" | "readonly"
// | "none" — providers are free to map these to whatever their API
// expects (Scaleway's API uses these exact strings).
type PrivilegeGranter interface {
	SetPrivilege(ctx context.Context, instanceID, databaseName, userName, permission string) error
}
