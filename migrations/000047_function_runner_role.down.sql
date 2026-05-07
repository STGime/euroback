-- 000047_function_runner_role.down.sql
--
-- Reverts per-tenant function-runner roles. Drops the per-tenant
-- `<schema>_func` roles for every tenant, then revokes runner role's
-- public-schema grants. Does NOT drop the eurobase_function_runner
-- login role itself — that's an externally-provisioned role (created
-- via Scaleway console) and the down direction must not delete it.
--
-- Re-applying 000040 will restore the older provision_tenant body
-- without the per-tenant role block. Existing tenants' func roles need
-- to be dropped explicitly here so the next provision attempt for the
-- same project_id doesn't fail with "role already exists".
--
-- This is intentionally lossy. Rolling back means accepting the
-- pre-fix cross-tenant SQL leak vector via ctx.db.unsafe.

BEGIN;

DO $$
DECLARE
    v_schema TEXT;
    v_role   TEXT;
BEGIN
    FOR v_schema IN
        SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        v_role := v_schema || '_func';
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_role) THEN
            -- Revoke first, then drop. REVOKE on a role that doesn't
            -- own anything is mostly a no-op, but DROP ROLE fails if
            -- the role still owns/has-grants on objects.
            EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %I FROM %I', v_schema, v_role);
            EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA %I FROM %I', v_schema, v_role);
            EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %I FROM %I', v_schema, v_role);
            EXECUTE format('REVOKE USAGE ON SCHEMA %I FROM %I', v_schema, v_role);
            EXECUTE format(
                'ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I
                 REVOKE ALL ON TABLES FROM %I',
                v_schema, v_role
            );
            EXECUTE format(
                'ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I
                 REVOKE ALL ON SEQUENCES FROM %I',
                v_schema, v_role
            );
            EXECUTE format('DROP ROLE IF EXISTS %I', v_role);
        END IF;
    END LOOP;
END$$;

-- Revoke runner's public-schema grants (the role itself stays).
REVOKE EXECUTE ON FUNCTION public.is_service_role() FROM eurobase_function_runner;
REVOKE EXECUTE ON FUNCTION public.current_end_user_id() FROM eurobase_function_runner;
REVOKE USAGE ON SCHEMA public FROM eurobase_function_runner;
REVOKE CONNECT ON DATABASE eurobase FROM eurobase_function_runner;

COMMIT;
