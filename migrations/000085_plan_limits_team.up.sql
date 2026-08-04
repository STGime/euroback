-- 000085_plan_limits_team.up.sql
--
-- Team-tier M2 of the closed-beta plan (milestone #2, issue #307).
-- Introduces the `team` row on `plan_limits` + three new binary
-- columns that only Team-tier projects flip on.
--
-- Pricing is deliberately NULL for the closed-beta window. When we
-- lock in a price, one UPDATE fills price_cents and the same
-- CreateCheckout code path routes non-beta users through Mollie
-- (see billing/service.go). During the beta, admin-granted users
-- flow through a `beta_grant` subscription row that skips Mollie
-- entirely.
--
-- The two nullable/defaulted PlanLimits additions (dedicated_db,
-- pitr_days, backup_retention_days) apply only to `team` for now.
-- Free + Pro get zero values; those tiers stay on the shared
-- cluster with no PITR / manual backup surface.
--
-- Rollout note: plans.LimitsService caches PlanLimits for process
-- lifetime, same window issue documented in 000075. CI runs the
-- migrate Job immediately before the gateway rolls, so the cache
-- cycles on the same deploy.

BEGIN;

-- Nullable price_cents so we can ship the tier before we lock in
-- a number. Non-null price + no beta flag is the path Mollie
-- checkout takes; NULL price + beta flag is the closed-beta path.
ALTER TABLE public.plan_limits
    ADD COLUMN IF NOT EXISTS price_cents           INT,
    ADD COLUMN IF NOT EXISTS dedicated_db          BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS pitr_days             INT     NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS backup_retention_days INT     NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS audit_log_retention_days INT  NOT NULL DEFAULT 0;

-- Backfill known prices for existing tiers. Kept in sync with the
-- planPriceCents map at internal/billing/service.go:52-55 so a
-- Mollie checkout for pro reads the same number the map has.
UPDATE public.plan_limits SET price_cents = 0     WHERE plan = 'free';
UPDATE public.plan_limits SET price_cents = 1900  WHERE plan = 'pro';

-- Insert the `team` row.
--
-- Cap philosophy:
--   * MAU: much higher than Pro — Team is production workloads.
--   * Storage / bandwidth: high, but not "unlimited"; a hard
--     ceiling reduces surprise egress bills.
--   * Realtime cxns: 10× Pro.
--   * All binary gates ON.
--   * Backups: 30-day retention, 7-day PITR — matches the plan.
--   * Audit log retention: 365 days (M2 default; the Legal-Team
--     tier under M2b will bump this to 3650 = 10y).
INSERT INTO public.plan_limits (
    plan, db_size_mb, storage_mb, bandwidth_mb, mau_limit,
    rate_limit_rps, ws_connections, upload_size_mb, webhook_limit,
    project_limit, log_retention_days, custom_templates,
    edge_function_limit, dsar_console_ui,
    custom_domain, byo_smtp, quota_alerts,
    price_cents, dedicated_db, pitr_days, backup_retention_days,
    audit_log_retention_days
) VALUES (
    'team',
    102400,   -- db_size_mb: 100 GB — starting point, resize online later
    512000,   -- storage_mb: 500 GB
    1048576,  -- bandwidth_mb: 1 TB / month
    1000000,  -- mau_limit: 1M
    5000,     -- rate_limit_rps: 5k
    50000,    -- ws_connections: 50k
    500,      -- upload_size_mb: 500 MB per upload
    0,        -- webhook_limit: unlimited (0 sentinel)
    50,       -- project_limit: 50 team projects per org
    90,       -- log_retention_days: 90 days
    true,     -- custom_templates
    250,      -- edge_function_limit
    true,     -- dsar_console_ui
    true,     -- custom_domain
    true,     -- byo_smtp
    true,     -- quota_alerts
    NULL,     -- price_cents: deliberately NULL during beta
    true,     -- dedicated_db: THE reason to be on Team
    7,        -- pitr_days
    30,       -- backup_retention_days
    365       -- audit_log_retention_days (M2b Legal-Team bumps to 3650)
)
ON CONFLICT (plan) DO NOTHING;

COMMENT ON COLUMN public.plan_limits.price_cents IS
    'Monthly price in cents. NULL means "not yet priced" — CreateCheckout refuses these plans unless the caller carries a beta grant (see internal/billing/handler.go).';
COMMENT ON COLUMN public.plan_limits.dedicated_db IS
    'True → each project on this tier gets a dedicated managed-PG instance provisioned via internal/dbprovider. False → tier stays on the shared cluster.';
COMMENT ON COLUMN public.plan_limits.pitr_days IS
    'Point-in-time recovery window in days. 0 = no PITR (Free + Pro).';
COMMENT ON COLUMN public.plan_limits.backup_retention_days IS
    'How long provider-scheduled + on-demand backups are retained. 0 = no user-visible backup surface (Free + Pro).';
COMMENT ON COLUMN public.plan_limits.audit_log_retention_days IS
    'Rows in public.audit_log for projects on this tier are retained this many days. 0 = never prune (backwards-compatible default; the retention worker only prunes when > 0).';

COMMIT;
