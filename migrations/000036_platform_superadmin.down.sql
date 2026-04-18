DROP INDEX IF EXISTS idx_platform_users_superadmin;
ALTER TABLE public.platform_users DROP COLUMN IF EXISTS is_superadmin;
