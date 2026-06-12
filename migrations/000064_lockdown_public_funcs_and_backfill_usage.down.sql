-- 000064_lockdown_public_funcs_and_backfill_usage.down.sql
--
-- Reverts 000064. WARNING: this re-opens the #217 privilege gap — it restores
-- the default EXECUTE -> PUBLIC on the privileged functions and removes the
-- per-tenant roles' USAGE on schema public (which also re-breaks #188 and the
-- migration bookkeeping path). Only run as part of a full rollback.

-- ── 1. Revoke the per-tenant USAGE backfill ─────────────────────────────
DO $$
DECLARE
    v_schema text;
    v_role   text;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        FOREACH v_role IN ARRAY ARRAY[v_schema || '_ddl', v_schema || '_func'] LOOP
            IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_role) THEN
                EXECUTE format('REVOKE USAGE ON SCHEMA public FROM %I', v_role);
            END IF;
        END LOOP;
    END LOOP;
END$$;

-- ── 2. Restore prior function-execute posture ───────────────────────────
ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA public
    GRANT EXECUTE ON FUNCTIONS TO PUBLIC;

REVOKE EXECUTE ON FUNCTION public.deprovision_tenant(uuid) FROM eurobase_gateway;

GRANT EXECUTE ON FUNCTION public.provision_tenant(uuid, text, text) TO PUBLIC;
GRANT EXECUTE ON FUNCTION public.deprovision_tenant(uuid)           TO PUBLIC;
GRANT EXECUTE ON FUNCTION public.provision_tenant_ddl_role(text)    TO PUBLIC;
