-- Ownership changes aren't reverted (the prior owner isn't recorded, and
-- _ddl ownership is the intended steady state that provision_tenant_ddl_role
-- re-establishes anyway). Drop only the helper.
DROP FUNCTION IF EXISTS public.converge_tenant_ownership(TEXT);
