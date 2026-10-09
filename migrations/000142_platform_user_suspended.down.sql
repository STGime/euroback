DROP INDEX IF EXISTS public.ix_platform_users_suspended;
ALTER TABLE public.platform_users
    DROP COLUMN IF EXISTS suspended_at,
    DROP COLUMN IF EXISTS suspended_reason,
    DROP COLUMN IF EXISTS suspended_by;
