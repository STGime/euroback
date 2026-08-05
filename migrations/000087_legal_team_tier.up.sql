-- 000087_legal_team_tier.up.sql
--
-- Team-tier M2b: introduces the **Legal Team** tier — a premium
-- add-on above base Team for German legal-tech, Steuerberater, and
-- legal-ops customers whose compliance obligations warrant the extra
-- price. Per the packaging decision recorded in
-- ~/.claude/plans/zazzy-booping-fountain.md (M2b note), the German
-- legal-tech compliance pack (§203 StGB / §43e BRAO paperwork,
-- retention-hold-aware erasure, GoBD export, 10-year audit retention,
-- staff secrecy register) is business-specific to that customer
-- segment — bundling it into base Team would either price the base
-- tier too high or under-price the legal-tier work.
--
-- Design:
--
--   * New tier row on plan_limits — inherits Team's numeric caps,
--     overrides audit_log_retention_days to 3650 (10 years, matches
--     §257 HGB / §147 AO retention obligations German customers are
--     subject to).
--   * Per-user beta gate on platform_users (team_beta_access got
--     its columns in 000084; here we add three more columns for
--     the legal-tier beta grants — parallel path so ops can grant
--     Team + Legal-Team independently).
--   * Price stays NULL for the beta window. billing/service.go
--     already routes NULL-priced plans through the RecordBetaGrant
--     path when the user carries the appropriate beta flag.

BEGIN;

-- Per-user gate for the Legal-Team closed beta. Same three-column
-- shape as 000084 (team_beta_access columns) for consistency.
ALTER TABLE public.platform_users
    ADD COLUMN legal_team_beta_access     BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN legal_team_beta_granted_at TIMESTAMPTZ,
    ADD COLUMN legal_team_beta_granted_by UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL;

CREATE INDEX ix_platform_users_legal_team_beta
    ON public.platform_users(legal_team_beta_granted_at DESC)
    WHERE legal_team_beta_access = true;

-- Insert the legal_team plan_limits row. Same numeric caps as team
-- (dedicated DB is the load-bearing infra difference; the compliance
-- features that justify the premium are gated on the plan code, not
-- on the caps).
INSERT INTO public.plan_limits (
    plan, db_size_mb, storage_mb, bandwidth_mb, mau_limit,
    rate_limit_rps, ws_connections, upload_size_mb, webhook_limit,
    project_limit, log_retention_days, custom_templates,
    edge_function_limit, dsar_console_ui,
    custom_domain, byo_smtp, quota_alerts,
    price_cents, dedicated_db, pitr_days, backup_retention_days,
    audit_log_retention_days
) VALUES (
    'legal_team',
    102400,   -- db_size_mb: 100 GB — same as team
    512000,   -- storage_mb: 500 GB
    1048576,  -- bandwidth_mb: 1 TB / month
    1000000,  -- mau_limit
    5000,     -- rate_limit_rps
    50000,    -- ws_connections
    500,      -- upload_size_mb
    0,        -- webhook_limit: unlimited
    50,       -- project_limit
    90,       -- log_retention_days
    true,     -- custom_templates
    250,      -- edge_function_limit
    true,     -- dsar_console_ui
    true,     -- custom_domain
    true,     -- byo_smtp
    true,     -- quota_alerts
    NULL,     -- price_cents: NULL during beta, premium over base Team when set
    true,     -- dedicated_db
    7,        -- pitr_days
    30,       -- backup_retention_days: same as team; separate legal-hold
              -- table (000088) covers the multi-year retention obligations
    3650      -- audit_log_retention_days: 10 years — matches §257 HGB /
              -- §147 AO retention obligations for German legal / tax /
              -- commercial records
)
ON CONFLICT (plan) DO NOTHING;

COMMIT;
