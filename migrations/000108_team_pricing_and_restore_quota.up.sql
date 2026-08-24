-- 000108_team_pricing_and_restore_quota.up.sql
--
-- Team-tier moves from "closed beta, price NULL, backup story
-- aspirational" to a real priced product with a defensible cost
-- model.
--
-- Decisions (locked before this migration was written):
--   * Team price: €149/mo per project.
--   * On-demand backups: REMOVED entirely (both HTTP surface and
--     the console button). PITR-to-just-before covers the "snapshot
--     before a risky migration" use case with the same semantics
--     and no additional storage cost.
--   * Restore charging shape: hard cap on included restores per
--     month; beyond the cap → 402 + support-conversation upsell.
--     Real usage-based billing (metered → next invoice line) is
--     deferred until Team crosses ~20 users where the manual path
--     stops scaling.
--
-- Cost model at Team's default SizeMedium (5-50GB):
--   Instance (db-gp-s):       ~€60/mo
--   Scheduled backups (7d):   ~€31/mo (50GB × 7 × €0.088/GB/mo)
--   Restore rollback (1/mo):  ~€14/mo amortized
--   ────────────────────────────────────
--   Total COGS:               ~€105/mo
--   Team price:               €149/mo
--   Gross margin:             ~30%
--
-- SizeLarge (>50GB) is NOT covered by standard Team pricing and
-- requires an enterprise conversation — separate future work.
-- Legal-Team keeps 30d retention as part of the compliance premium
-- (price stays NULL — closed beta — until we decide the number).
--
-- Interaction with #459 (reconcile-backup-schedule sweeper):
--   The sweeper picks up plan_limits.backup_retention_days at
--   execution time and calls Provider.SetBackupSchedule with the
--   current value. Existing active rows already have
--   backup_schedule_applied_at stamped from the 30-day-era tick,
--   so a data update alone wouldn't force a re-apply. This
--   migration ALSO resets backup_schedule_applied_at=NULL on all
--   active rows so the next sweep tick reconciles to 7d.

BEGIN;

-- ── Schema: two new columns on plan_limits ──────────────────────
ALTER TABLE public.plan_limits
    -- included_restores_per_month → the handler counts
    -- restore_operations for this project in the current calendar
    -- month; enqueue is rejected with 402 restore_quota_exceeded
    -- once this cap is reached. 0 = feature disabled for the plan
    -- (Free / Pro — no dedicated DB, no restore surface).
    ADD COLUMN IF NOT EXISTS included_restores_per_month INT NOT NULL DEFAULT 0,

    -- on_demand_backups_enabled → feature flag, read by the router
    -- and (defensively) by HandleCreateBackup. Distinct from
    -- "no route registered" so a future rollback path can flip it
    -- back without a code deploy. Kept FALSE for all plans in this
    -- migration; there is no user-visible on-demand button anymore.
    ADD COLUMN IF NOT EXISTS on_demand_backups_enabled BOOLEAN NOT NULL DEFAULT false;

-- ── Data: Team gets a real price, tighter retention, quota ──────
UPDATE public.plan_limits
   SET price_cents                 = 14900,   -- €149.00
       backup_retention_days       = 7,       -- was 30 — matches Scaleway's likely default
       included_restores_per_month = 1,       -- safety-net restore each month
       on_demand_backups_enabled   = false
 WHERE plan = 'team';

-- Legal-Team: keeps 30d retention (compliance framing — DR
-- insurance beyond what WORM + retention holds provide). Restore
-- quota matches Team; a compliance-workflow restore path with
-- audit-log integration is separate future work. Price stays NULL
-- (closed beta) until we decide the number.
UPDATE public.plan_limits
   SET included_restores_per_month = 1,
       on_demand_backups_enabled   = false
 WHERE plan = 'legal_team';

-- ── Force reconciliation of the scheduled-backup retention ──────
-- Active dedicated instances currently carry the old 30-day
-- schedule Scaleway-side. The #459 sweeper skips rows where
-- backup_schedule_applied_at IS NOT NULL, so a plain data update
-- to plan_limits wouldn't take effect. Reset the stamp on every
-- active row; the next hourly sweep re-applies the (now 7-day)
-- schedule via Provider.SetBackupSchedule.
--
-- Safe: SetBackupSchedule is idempotent on Scaleway; the reconcile
-- worker refuses zero-retention (matches #456's floor). At current
-- prod scale (0 active Team rows per today's sweep) this is a
-- no-op; migration is written for the general case.
UPDATE public.project_databases
   SET backup_schedule_applied_at = NULL
 WHERE state = 'active' AND deleted_at IS NULL;

COMMIT;
