-- 000043_eurobase_developer_role.up.sql
--
-- Adds a third runtime Postgres role: eurobase_developer.
--
--   eurobase_developer — login role used by the gateway's *platform-
--                        authenticated* DB pool (DATABASE_URL_DEVELOPER).
--                        Member of eurobase_migrator with INHERIT, so it
--                        gets ownership-equivalent privileges on every
--                        tenant schema and on public.*.
--
-- Why a separate role and not just reuse eurobase_migrator directly:
-- physical pool separation. The runtime gateway pool stays on
-- eurobase_gateway (DML only). The platform-developer pool runs on
-- eurobase_developer. Even if a runtime exploit reaches the gateway
-- process, it has no path to elevated privileges because the runtime
-- pool's connection cannot SET ROLE migrator (gateway is not a member).
--
-- The platform pool's transactions issue `SET LOCAL ROLE eurobase_migrator`
-- at tx start so any newly-created tables are owned by the migrator —
-- consistent with tables created by the CI migrate Job and avoiding a
-- per-developer ownership patchwork.
--
-- The role itself MUST be created via the Scaleway console BEFORE this
-- migration runs (mirrors the bootstrap requirement for migrator/gateway
-- in 000037).

BEGIN;

-- Fail fast if the role is missing.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        RAISE EXCEPTION 'role eurobase_developer does not exist — create it via the Scaleway console first (CREATE ROLE eurobase_developer WITH LOGIN INHERIT)';
    END IF;
END$$;

-- Idempotency: this migration may be re-run during recovery; skip the
-- grant if it's already in place.
GRANT eurobase_migrator TO eurobase_developer;

GRANT CONNECT ON DATABASE eurobase TO eurobase_developer;

-- Belt-and-suspender: ensure the existing default-privileges block from
-- 000037 covers eurobase_gateway for tables the developer creates while
-- in the migrator role. (When the platform pool runs SET LOCAL ROLE
-- eurobase_migrator, CREATE TABLE produces migrator-owned tables, and
-- the existing ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator block
-- already grants DML to eurobase_gateway. Nothing to add here unless the
-- block was lost.)

-- Sanity log to migrate Job output.
DO $$
BEGIN
    RAISE NOTICE 'eurobase_developer is now a member of eurobase_migrator; platform pool can use DATABASE_URL_DEVELOPER';
END$$;

COMMIT;
