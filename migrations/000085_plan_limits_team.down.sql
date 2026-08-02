-- 000085_plan_limits_team.down.sql

BEGIN;

DELETE FROM public.plan_limits WHERE plan = 'team';

ALTER TABLE public.plan_limits
    DROP COLUMN IF EXISTS audit_log_retention_days,
    DROP COLUMN IF EXISTS backup_retention_days,
    DROP COLUMN IF EXISTS pitr_days,
    DROP COLUMN IF EXISTS dedicated_db,
    DROP COLUMN IF EXISTS price_cents;

COMMIT;
