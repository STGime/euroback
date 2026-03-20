-- 000002_tenant_functions.down.sql
-- Drop tenant isolation functions.

BEGIN;

DROP FUNCTION IF EXISTS public.deprovision_tenant(UUID);
DROP FUNCTION IF EXISTS public.provision_tenant(UUID, TEXT, TEXT);
DROP FUNCTION IF EXISTS public.set_tenant_id(UUID);

COMMIT;
