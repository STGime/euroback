-- 000142_platform_user_suspended.up.sql
--
-- Account-level suspension for platform users. A suspended account cannot
-- sign in (any login path) and cannot create projects — closing the gap
-- where suspending a user's projects + deleting their tokens still left the
-- account able to sign in and create a fresh project. Enforcement is in Go
-- (generatePlatformJWT refuses to mint a session; CreateProject refuses).
--
-- suspended_by references the superadmin who suspended; NULL for an ops /
-- migration action. No grant changes: eurobase_gateway and eurobase_developer
-- already hold table-level SELECT on platform_users (and gateway has blanket
-- table DML from 000037). No explicit BEGIN/COMMIT: golang-migrate wraps the file.

ALTER TABLE public.platform_users
    ADD COLUMN IF NOT EXISTS suspended_at     TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS suspended_reason TEXT CHECK (length(suspended_reason) <= 500),
    ADD COLUMN IF NOT EXISTS suspended_by     UUID REFERENCES public.platform_users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS ix_platform_users_suspended
    ON public.platform_users (suspended_at) WHERE suspended_at IS NOT NULL;
