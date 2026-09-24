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

-- ensure_data_access_log_partition (000066) is SECURITY DEFINER, owned by
-- the migrator, and was left with PUBLIC EXECUTE and a search_path
-- without pg_temp. Only the gateway (explicit grant in 000066) and
-- migrator-owned maintenance code call it.
REVOKE ALL ON FUNCTION public.ensure_data_access_log_partition(date) FROM PUBLIC;
ALTER FUNCTION public.ensure_data_access_log_partition(date) SET search_path = public, pg_temp;

-- Fail loud rather than silently keeping a grant made by another grantor
-- (REVOKE only warns in that case). Tables, partitioned tables and
-- sequences only — pgTAP helper views owned by _rdb_superadmin are not
-- ours to change and expose nothing.
DO $$
DECLARE
    leftover text;
BEGIN
    SELECT string_agg(c.relname || ' (grantor ' || pg_get_userbyid(a.grantor) || ')', ', ')
      INTO leftover
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace,
           aclexplode(c.relacl) a
     WHERE n.nspname = 'public'
       AND c.relkind IN ('r', 'p', 'S')
       AND a.grantee = 'eurobase_function_runner'::regrole;
    IF leftover IS NOT NULL THEN
        RAISE EXCEPTION 'eurobase_function_runner still has privileges on public objects: %', leftover;
    END IF;
    IF has_function_privilege('eurobase_function_runner', 'public.provision_tenant(uuid,text,text)', 'EXECUTE')
       OR has_function_privilege('eurobase_function_runner', 'public.deprovision_tenant(uuid)', 'EXECUTE')
       OR has_function_privilege('public', 'public.ensure_data_access_log_partition(date)', 'EXECUTE') THEN
        RAISE EXCEPTION 'lifecycle / partition function EXECUTE was not revoked';
    END IF;
END$$;
