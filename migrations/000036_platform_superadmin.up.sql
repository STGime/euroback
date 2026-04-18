-- 000036_platform_superadmin.up.sql
-- Introduce a superadmin flag on platform_users. Superadmins are Eurobase
-- staff who can run platform-level operations (migrations, cross-tenant
-- admin) from the console. Regular project owners manage only their own
-- projects and must never hold credentials that bypass tenant isolation.

ALTER TABLE public.platform_users
    ADD COLUMN IF NOT EXISTS is_superadmin BOOLEAN NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS idx_platform_users_superadmin
    ON public.platform_users(is_superadmin) WHERE is_superadmin;
