-- Ownership changes aren't reverted (the prior owner isn't recorded, and the
-- reassigned set matches what provision_tenant_ddl_role already does on every
-- migrate up). Drop only the helper.
DROP FUNCTION IF EXISTS public.converge_tenant_ownership(TEXT);
