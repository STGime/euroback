-- 000008_platform_auth.down.sql
BEGIN;

DROP TABLE IF EXISTS public.platform_config;

ALTER TABLE public.projects DROP COLUMN IF EXISTS jwt_secret;

DROP INDEX IF EXISTS idx_platform_users_email;
ALTER TABLE public.platform_users ALTER COLUMN hanko_user_id SET NOT NULL;
ALTER TABLE public.platform_users
    DROP COLUMN IF EXISTS password_hash,
    DROP COLUMN IF EXISTS email_confirmed_at,
    DROP COLUMN IF EXISTS last_sign_in_at;

COMMIT;
