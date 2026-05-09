-- 000048_auth_compat_schema.down.sql
--
-- Reverse 000048: drop the auth.* compat helpers. Any RLS policies that
-- reference auth.uid()/auth.role()/auth.email()/auth.jwt() must be
-- rewritten to use the public/auth_uid equivalents BEFORE running this
-- down migration. The drop is intentionally not CASCADE so a stray
-- dependency raises a clear error rather than silently destroying
-- policies.

BEGIN;

DROP FUNCTION IF EXISTS auth.jwt();
DROP FUNCTION IF EXISTS auth.email();
DROP FUNCTION IF EXISTS auth.role();
DROP FUNCTION IF EXISTS auth.uid();
DROP SCHEMA IF EXISTS auth;

COMMIT;
