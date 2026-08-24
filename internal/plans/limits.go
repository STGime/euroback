// Package plans provides plan limits, usage tracking, and enforcement for Eurobase projects.
package plans

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PlanLimits represents the resource limits for a billing plan.
type PlanLimits struct {
	Plan             string `json:"plan"`
	DBSizeMB         int    `json:"db_size_mb"`
	StorageMB        int    `json:"storage_mb"`
	BandwidthMB      int    `json:"bandwidth_mb"`
	MAULimit         int    `json:"mau_limit"`
	RateLimitRPS     int    `json:"rate_limit_rps"`
	WSConnections    int    `json:"ws_connections"`
	UploadSizeMB     int    `json:"upload_size_mb"`
	WebhookLimit     int    `json:"webhook_limit"`
	ProjectLimit     int    `json:"project_limit"`
	LogRetentionDays  int    `json:"log_retention_days"`
	CustomTemplates   bool   `json:"custom_templates"`
	EdgeFunctionLimit int    `json:"edge_function_limit"`

	// DSARConsoleUI gates the one-click Compliance → Data Export
	// console tab (#251, part of #248). NOT a gate on the underlying
	// API endpoints — DSAR is a legal obligation and the export
	// endpoints stay callable on every tier; the console flow is the
	// upsell. Free = false, Pro = true. See migration 000072 +
	// docs/compliance/dsar-soft-gate.md.
	DSARConsoleUI bool `json:"dsar_console_ui"`

	// Phase B binary Pro-only gates (migration 000075).
	CustomDomain bool `json:"custom_domain"` // CNAME your own domain to the project's REST + Auth surface.
	BYOSMTP      bool `json:"byo_smtp"`      // Bring your own SMTP for auth mail.
	QuotaAlerts  bool `json:"quota_alerts"`  // Slack / webhook alerts at 80% of any quota.

	// Team-tier fields (migration 000085). Pricing + capability
	// flags that only apply to Team (and future Legal-Team) plans.
	// PriceCents is nullable in the DB — a NULL surfaces here as
	// nil so the billing service can refuse checkout with a clear
	// "plan not priced yet, requires beta grant" error.
	PriceCents            *int `json:"price_cents,omitempty"`
	DedicatedDB           bool `json:"dedicated_db"`
	PITRDays              int  `json:"pitr_days"`
	BackupRetentionDays   int  `json:"backup_retention_days"`
	AuditLogRetentionDays int  `json:"audit_log_retention_days"`

	// Migration 000108 (backup cost model rework):
	//   IncludedRestoresPerMonth — hard cap enforced by
	//     HandleCreateRestore. Once reached, further restores return
	//     402 restore_quota_exceeded until the calendar month rolls
	//     over. 0 = feature disabled (Free/Pro have no restore surface).
	//   OnDemandBackupsEnabled — false on all plans today; kept as a
	//     column (not a code const) so a future rollback path can
	//     flip it without a deploy.
	IncludedRestoresPerMonth int  `json:"included_restores_per_month"`
	OnDemandBackupsEnabled   bool `json:"on_demand_backups_enabled"`
}

// legacyFreeLimits returns a PlanLimits struct with the pre-Phase-B
// Free-tier cap values. Used by GetEffectiveProjectLimits when a
// project's `grandfathered_until` window is still open: existing beta
// users are held at the OLD numbers for 90 days after Phase B lands
// so we don't retroactively break them (public-beta launch plan
// decision #3, migration 000076).
//
// Only the four caps that Phase B halved are restored here; the
// binary Pro-only gates (custom_domain, byo_smtp, quota_alerts) stay
// off — those didn't exist before Phase B and enabling them for
// grandfathered Free projects would be a real product change, not a
// grandfather.
func legacyFreeLimits(current *PlanLimits) *PlanLimits {
	copy := *current // shallow copy — all fields are value types
	copy.MAULimit = 10000
	copy.StorageMB = 1024
	copy.BandwidthMB = 5120
	copy.WSConnections = 100
	return &copy
}

// PoolResolver returns the pool that owns a project's tenant schema:
// the project's dedicated managed-PG instance for Team-tier, nil for
// Free/Pro (caller falls back to the shared platform pool). Set via
// LimitsService.WithPoolResolver from the gateway boot path.
type PoolResolver func(ctx context.Context, projectID string) *pgxpool.Pool

// LimitsService provides plan limit lookups with in-memory caching.
type LimitsService struct {
	pool         *pgxpool.Pool
	poolResolver PoolResolver
	mu           sync.RWMutex
	cache        map[string]*PlanLimits
}

// NewLimitsService creates a new LimitsService backed by the given connection pool.
func NewLimitsService(pool *pgxpool.Pool) *LimitsService {
	return &LimitsService{
		pool:  pool,
		cache: make(map[string]*PlanLimits),
	}
}

// WithPoolResolver wires a Team-tier-aware pool resolver so tenant-
// schema queries (db size / storage size / MAU count) target the
// project's dedicated instance instead of the shared platform DB
// (where the schema does not exist for Team-tier). Free/Pro projects
// return nil from the resolver and stay on the shared pool.
func (s *LimitsService) WithPoolResolver(r PoolResolver) *LimitsService {
	s.poolResolver = r
	return s
}

// tenantPool picks the dedicated pool for projectID when the resolver
// is wired and the project has one, otherwise falls back to the shared
// platform pool.
func (s *LimitsService) tenantPool(ctx context.Context, projectID string) *pgxpool.Pool {
	if s.poolResolver != nil {
		if p := s.poolResolver(ctx, projectID); p != nil {
			return p
		}
	}
	return s.pool
}

// GetLimits returns the limits for the given plan name. Results are cached in memory.
func (s *LimitsService) GetLimits(ctx context.Context, plan string) (*PlanLimits, error) {
	// Check cache first.
	s.mu.RLock()
	if cached, ok := s.cache[plan]; ok {
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	// Query database.
	var l PlanLimits
	err := s.pool.QueryRow(ctx,
		`SELECT plan, db_size_mb, storage_mb, bandwidth_mb, mau_limit,
		        rate_limit_rps, ws_connections, upload_size_mb, webhook_limit,
		        project_limit, log_retention_days, custom_templates, edge_function_limit,
		        dsar_console_ui, custom_domain, byo_smtp, quota_alerts,
		        price_cents, dedicated_db, pitr_days, backup_retention_days,
		        audit_log_retention_days,
		        included_restores_per_month, on_demand_backups_enabled
		 FROM plan_limits WHERE plan = $1`, plan,
	).Scan(
		&l.Plan, &l.DBSizeMB, &l.StorageMB, &l.BandwidthMB, &l.MAULimit,
		&l.RateLimitRPS, &l.WSConnections, &l.UploadSizeMB, &l.WebhookLimit,
		&l.ProjectLimit, &l.LogRetentionDays, &l.CustomTemplates, &l.EdgeFunctionLimit,
		&l.DSARConsoleUI, &l.CustomDomain, &l.BYOSMTP, &l.QuotaAlerts,
		&l.PriceCents, &l.DedicatedDB, &l.PITRDays, &l.BackupRetentionDays,
		&l.AuditLogRetentionDays,
		&l.IncludedRestoresPerMonth, &l.OnDemandBackupsEnabled,
	)
	if err != nil {
		slog.Error("failed to load plan limits", "plan", plan, "error", err)
		return nil, fmt.Errorf("plan %q not found: %w", plan, err)
	}

	// Cache the result.
	s.mu.Lock()
	s.cache[plan] = &l
	s.mu.Unlock()

	slog.Debug("plan limits loaded", "plan", plan)
	return &l, nil
}

// GetProjectLimits looks up the plan for a project and returns its
// CURRENT-tier limits.
//
// Prefer `GetEffectiveProjectLimits` for enforcement paths that touch
// caps the Phase B tightening changed (MAU / storage / bandwidth /
// realtime connections). This raw variant is fine for binary gates
// (custom templates, DSAR console UI, custom domain, BYO SMTP, quota
// alerts) — those didn't exist on the pre-Phase-B plan so
// grandfathering doesn't apply.
func (s *LimitsService) GetProjectLimits(ctx context.Context, projectID string) (*PlanLimits, error) {
	var plan string
	err := s.pool.QueryRow(ctx,
		`SELECT plan FROM projects WHERE id = $1`, projectID,
	).Scan(&plan)
	if err != nil {
		slog.Error("failed to look up project plan", "project_id", projectID, "error", err)
		return nil, fmt.Errorf("project %q not found: %w", projectID, err)
	}

	return s.GetLimits(ctx, plan)
}

// GetEffectiveProjectLimits returns the limits an enforcement check
// should apply to `projectID`. Same as GetProjectLimits for Pro
// projects and for Free projects created after Phase B landed. For
// Free projects still inside their `grandfathered_until` window it
// returns the LEGACY (pre-Phase-B) cap values so existing beta users
// aren't retroactively broken by the tightening in migration 000075.
//
// Only the four halved caps (MAU / storage / bandwidth / realtime
// connections) are restored — see `legacyFreeLimits` for why.
func (s *LimitsService) GetEffectiveProjectLimits(ctx context.Context, projectID string) (*PlanLimits, error) {
	var (
		plan               string
		grandfatheredUntil *time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT plan, grandfathered_until FROM projects WHERE id = $1`, projectID,
	).Scan(&plan, &grandfatheredUntil)
	if err != nil {
		slog.Error("failed to look up project plan", "project_id", projectID, "error", err)
		return nil, fmt.Errorf("project %q not found: %w", projectID, err)
	}

	current, err := s.GetLimits(ctx, plan)
	if err != nil {
		return nil, err
	}
	if plan == "free" && grandfatheredUntil != nil && grandfatheredUntil.After(time.Now()) {
		return legacyFreeLimits(current), nil
	}
	return current, nil
}

// IsPaidPlan reports whether the plan string names a paid tier.
// Any unknown value is treated as unpaid (fail-closed) so a schema
// drift or typo can't accidentally unlock paid-only features.
//
// Used by the SMS-auth gate (#329): every phone-OTP send costs real
// money via GatewayAPI, so we lock the channel to paid tiers.
func IsPaidPlan(plan string) bool {
	switch plan {
	case "pro", "team", "legal_team":
		return true
	default:
		return false
	}
}
