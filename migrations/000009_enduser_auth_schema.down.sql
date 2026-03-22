-- 000009_enduser_auth_schema.down.sql
-- Revert provision_tenant() to the version from 000007.
-- Note: existing tenant schemas are not altered; only new schemas will differ.
BEGIN;

-- Restore the 000007 version of provision_tenant (without auth columns).
-- This is a no-op for existing schemas.

COMMIT;
