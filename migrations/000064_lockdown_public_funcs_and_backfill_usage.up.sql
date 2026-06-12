-- 000064_lockdown_public_funcs_and_backfill_usage.up.sql
--
-- Closes the privilege gap surfaced by #217.
--
-- The per-tenant _ddl/_func roles need USAGE ON SCHEMA public (for migration
-- bookkeeping — public.tenant_migration_checksum / record_tenant_migration —
-- and for #188 RLS-helper resolution: public.is_service_role() etc.). But
-- granting schema USAGE to those tenant-CONTROLLED roles is only safe once the
-- privileged SECURITY DEFINER functions in public are locked down:
-- provision_tenant / deprovision_tenant / provision_tenant_ddl_role are owned
-- by eurobase_migrator and still carry Postgres's default EXECUTE -> PUBLIC, so
-- a role with schema USAGE (every role is a member of PUBLIC) could call e.g.
-- `public.deprovision_tenant('<other tenant>')` from inside a migration DO/
-- function body (invisible to the validator) and run it as migrator. So:
-- revoke PUBLIC execute FIRST, then backfill USAGE.
--
-- Requires Scaleway to have run, as the database / _rdb_superadmin owner:
--   GRANT USAGE ON SCHEMA public TO eurobase_migrator WITH GRANT OPTION;
-- (mirrors the CONNECT precedent). Without it migrator's GRANT USAGE is a
-- silent no-op, so step 0 verifies the grant option up front and fails loud.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

-- ── 0. Fail loud if Scaleway hasn't granted the option ──────────────────
DO $$
BEGIN
    IF NOT has_schema_privilege('eurobase_migrator', 'public', 'USAGE WITH GRANT OPTION') THEN
        RAISE EXCEPTION
          'eurobase_migrator lacks USAGE ... WITH GRANT OPTION on schema public — ask Scaleway to run, as the database owner / _rdb_superadmin: GRANT USAGE ON SCHEMA public TO eurobase_migrator WITH GRANT OPTION; (see #217)';
    END IF;
END$$;

-- ── 1. Lock down the privileged SECURITY DEFINER functions in public ────
-- Default Postgres EXECUTE -> PUBLIC means any role with schema USAGE can call
-- these as migrator. Revoke PUBLIC, then re-grant only the actual callers.
REVOKE EXECUTE ON FUNCTION public.provision_tenant(uuid, text, text) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.deprovision_tenant(uuid)           FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.provision_tenant_ddl_role(text)    FROM PUBLIC;

-- provision_tenant is created on the gateway pool (internal/tenant/service.go)
-- — keep its existing explicit grant.
GRANT EXECUTE ON FUNCTION public.provision_tenant(uuid, text, text) TO eurobase_gateway;
-- deprovision_tenant is ALSO called on the gateway pool today, but only via the
-- PUBLIC default we just removed — grant it explicitly or project deletion 500s.
GRANT EXECUTE ON FUNCTION public.deprovision_tenant(uuid) TO eurobase_gateway;
-- provision_tenant_ddl_role is only ever invoked as migrator (from
-- provision_tenant and the backfills) — owner privilege suffices, no grant.

-- Belt-and-suspenders: a future migrator-created function in public must not be
-- PUBLIC-executable by default, so a forgotten REVOKE can't silently reopen the
-- gap once tenant roles hold schema USAGE.
ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA public
    REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

-- ── 2. Backfill USAGE ON SCHEMA public for every per-tenant role ────────
-- New tenants already get this in provision_tenant / provision_tenant_ddl_role
-- (000063); this closes EXISTING tenants. Safe now that the privileged
-- functions above are locked down. _func USAGE is what closes #188 in prod.
DO $$
DECLARE
    v_schema text;
    v_ddl    text;
    v_func   text;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        v_ddl  := v_schema || '_ddl';
        v_func := v_schema || '_func';
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_ddl) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', v_ddl);
        END IF;
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_func) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', v_func);
        END IF;
    END LOOP;
END$$;

-- ── 3. Verify the backfill actually took (loud, not silent) ─────────────
DO $$
DECLARE
    v_schema text;
    v_role   text;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        FOREACH v_role IN ARRAY ARRAY[v_schema || '_ddl', v_schema || '_func'] LOOP
            IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_role)
               AND NOT has_schema_privilege(v_role, 'public', 'USAGE') THEN
                RAISE EXCEPTION
                  'role % still lacks USAGE on schema public after backfill (migrator grant option missing?) — see #217', v_role;
            END IF;
        END LOOP;
    END LOOP;
END$$;
