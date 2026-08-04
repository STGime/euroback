-- 000084_team_beta_access.down.sql

BEGIN;

DROP INDEX IF EXISTS public.ix_platform_users_team_beta;

ALTER TABLE public.platform_users
    DROP COLUMN IF EXISTS team_beta_granted_by,
    DROP COLUMN IF EXISTS team_beta_granted_at,
    DROP COLUMN IF EXISTS team_beta_access;

COMMIT;
