-- 000108_team_pricing_and_restore_quota.down.sql
--
-- Rollback: revert the plan_limits data changes then drop the
-- new columns. The backup_schedule_applied_at reset is NOT
-- undone — after a down migration + code rollback, the next
-- reconcile sweep tick would re-apply the OLD (30d) schedule
-- via SetBackupSchedule. Idempotent on the provider side.

BEGIN;

-- Revert data first (must run before dropping the columns).
UPDATE public.plan_limits
   SET price_cents           = NULL,
       backup_retention_days = 30
 WHERE plan = 'team';

-- Legal-Team retention was unchanged; nothing to revert there
-- besides the new columns going away with the drop below.

ALTER TABLE public.plan_limits
    DROP COLUMN IF EXISTS on_demand_backups_enabled,
    DROP COLUMN IF EXISTS included_restores_per_month;

COMMIT;
