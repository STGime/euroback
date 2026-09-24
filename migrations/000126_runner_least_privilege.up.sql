-- 000126_runner_least_privilege.up.sql
--
-- Step 2 of 2 after 000125: the edge-functions runner now loads code only
-- through public.runner_get_function and vault secrets through
-- public.vault_get_for_runner, so its login role needs no direct access
-- to platform tables. Remove what it still holds on prod (historic
-- grants, not created by any migration — fresh databases never had them,
-- so this is a no-op there).
--
-- Also locks the two tenant-lifecycle SECURITY DEFINER functions to the
-- gateway, which is the only caller (internal/tenant/service.go). Prod
-- already had the PUBLIC revoke applied by hand; fresh databases did not,
-- which violated the SECURITY DEFINER convention in CLAUDE.md.
--
-- Not touched here: default privileges owned by _rdb_superadmin (not
-- ours to change) — they only apply to objects that role creates.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

REVOKE ALL ON ALL TABLES    IN SCHEMA public FROM eurobase_function_runner;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM eurobase_function_runner;

REVOKE ALL ON FUNCTION public.provision_tenant(uuid, text, text) FROM PUBLIC, eurobase_function_runner;
REVOKE ALL ON FUNCTION public.deprovision_tenant(uuid)          FROM PUBLIC, eurobase_function_runner;
GRANT EXECUTE ON FUNCTION public.provision_tenant(uuid, text, text) TO eurobase_gateway;
GRANT EXECUTE ON FUNCTION public.deprovision_tenant(uuid)          TO eurobase_gateway;
