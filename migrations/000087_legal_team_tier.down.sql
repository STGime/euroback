-- 000087_legal_team_tier.down.sql

BEGIN;

DELETE FROM public.plan_limits WHERE plan = 'legal_team';

DROP INDEX IF EXISTS public.ix_platform_users_legal_team_beta;

ALTER TABLE public.platform_users
    DROP COLUMN IF EXISTS legal_team_beta_granted_by,
    DROP COLUMN IF EXISTS legal_team_beta_granted_at,
    DROP COLUMN IF EXISTS legal_team_beta_access;

COMMIT;
