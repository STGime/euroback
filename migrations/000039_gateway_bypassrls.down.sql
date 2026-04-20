-- 000039_gateway_bypassrls.down.sql
-- Revoke the BYPASSRLS attribute from eurobase_gateway. Platform-admin
-- paths that depended on `SET LOCAL row_security = off` will fail
-- silently (RLS re-applies) until policies on user-created tables are
-- back-filled with the service-role branch.

BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_gateway') THEN
        EXECUTE 'ALTER ROLE eurobase_gateway NOBYPASSRLS';
    END IF;
END$$;

COMMIT;
