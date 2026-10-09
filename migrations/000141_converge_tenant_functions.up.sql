-- 000141_converge_tenant_functions.up.sql
--
-- Step 6c (continued): move migrator-owned FUNCTIONS, views and standalone
-- types in a tenant schema to the project's own `<schema>_ddl` role, so that
-- once console SQL runs as `_ddl` (step 6d) a developer can edit their own
-- objects (CREATE OR REPLACE needs ownership), and so that an existing
-- SECURITY DEFINER function stops running as `eurobase_migrator`.
--
-- ZERO-FAILURE DESIGN (a reassigned function must never start failing):
--   A SECURITY DEFINER function runs as its OWNER. Reassigning migrator -> _ddl
--   makes it run as `_ddl`, which can reach only its OWN schema. So, in one
--   pass per schema:
--     1. Grant `_ddl` the complete in-schema superset FIRST — the same DML on
--        its system tables that `_func` has, EXECUTE on the public platform
--        helpers, USAGE on public — so anything a legitimate in-schema
--        function does still works as `_ddl`. (`_ddl` already owns the app
--        tables/sequences; 000136 / 000063 handle those.)
--     2. Reassign a function/view/type to `_ddl` ONLY if its body stays inside
--        its own schema + public helpers — i.e. it does NOT reference a
--        platform table, another tenant schema, or a privileged system
--        catalog. Anything that does (the planted-probe class) is LEFT
--        migrator-owned and only warned; it is never silently broken.
--   The guarantee: a reassigned object can only fail if it reaches outside its
--   own schema; the gate reassigns nothing that does; therefore no legitimate
--   object fails. The excluded class keeps its current owner (unchanged).
--
-- The platform RLS helpers auth_uid/auth_role/auth_email stay migrator-owned
-- (they are platform-provided, re-granted as needed). System tables stay
-- migrator-owned (only their DML is granted to `_ddl`).
--
-- lock_timeout bounds contention like 000136; each object is reassigned in a
-- subtransaction so one failure is skipped, not fatal. golang-migrate wraps
-- the file in one transaction. No explicit BEGIN/COMMIT.

CREATE OR REPLACE FUNCTION public.converge_tenant_functions(p_schema TEXT)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $conv$
DECLARE
    v_ddl_role  TEXT := p_schema || '_ddl';
    v_func_role TEXT := p_schema || '_func';
    v_system_tables TEXT[] := ARRAY[
        'users', 'user_identities', 'refresh_tokens', 'email_tokens',
        'storage_objects', 'storage_shared_prefixes', 'vault_secrets',
        'user_passkey_credentials', 'webauthn_challenges'
    ];
    -- A body referencing any of these reaches outside its own schema, so the
    -- object is NOT reassigned (left migrator-owned, warned). Case-insensitive.
    v_platform_refs TEXT := '(platform_users|platform_sql_log|platform_allowlist|platform_passkey'
        || '|public\.projects|api_keys|personal_access_tokens|org_members|organizations|org_invitations'
        || '|public\.subscriptions|audit_log|data_access_log|schema_changes|request_logs|sub_processors'
        || '|support_requests|contact_requests|project_databases|project_upgrades|tenant_migrations'
        || '|pg_authid|pg_shadow|pg_stat_activity|pg_stat_get_activity|pg_read_file|pg_read_binary_file'
        || '|pg_ls_dir|pg_ls_logdir|pg_ls_waldir|pg_hba_file_rules|pg_file_settings|pg_settings'
        || '|pg_roles|pg_user|pg_database|pg_stat_statements|pg_catalog\.pg_authid)';
    v_helpers TEXT[] := ARRAY['is_service_role', 'current_end_user_id', 'is_internal_auth_path'];
    v_h          TEXT;
    v_rel        RECORD;
    v_src        TEXT;
    v_reassigned INT := 0;
    v_left       INT := 0;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_ddl_role)
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_func_role) THEN
        RAISE WARNING 'converge_tenant_functions: role missing for %, skipping', p_schema;
        RETURN;
    END IF;

    SET LOCAL lock_timeout = '3s';

    -- 1. Superset for `_ddl`: DML on its own system tables (+ their sequences),
    --    USAGE on public, EXECUTE on the public platform helpers. Idempotent.
    EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', v_ddl_role);
    FOR v_rel IN
        SELECT c.relname, c.relkind FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = p_schema
          AND ((c.relkind IN ('r','p') AND c.relname = ANY(v_system_tables))
            OR (c.relkind = 'S' AND EXISTS (
                SELECT 1 FROM pg_depend d JOIN pg_class t ON t.oid = d.refobjid AND d.refclassid = 'pg_class'::regclass
                WHERE d.objid = c.oid AND d.deptype IN ('a','i') AND t.relname = ANY(v_system_tables))))
    LOOP
        BEGIN
            IF v_rel.relkind = 'S' THEN
                EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %I.%I TO %I', p_schema, v_rel.relname, v_ddl_role);
            ELSE
                EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I.%I TO %I', p_schema, v_rel.relname, v_ddl_role);
            END IF;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'converge_tenant_functions: grant _ddl on %.% failed: %', p_schema, v_rel.relname, SQLERRM;
        END;
    END LOOP;
    FOREACH v_h IN ARRAY v_helpers LOOP
        BEGIN
            EXECUTE format('GRANT EXECUTE ON FUNCTION public.%I() TO %I', v_h, v_ddl_role);
        EXCEPTION WHEN undefined_function THEN NULL;
        WHEN OTHERS THEN
            RAISE WARNING 'converge_tenant_functions: grant EXECUTE public.%() to _ddl failed: %', v_h, SQLERRM;
        END;
    END LOOP;
    -- uuid_generate_v4 is the one known Scaleway grant gotcha; try, don't fail.
    BEGIN
        EXECUTE format('GRANT EXECUTE ON FUNCTION public.uuid_generate_v4() TO %I', v_ddl_role);
    EXCEPTION WHEN OTHERS THEN NULL;
    END;

    -- 2. Reassign migrator-owned FUNCTIONS whose body stays in-schema.
    FOR v_rel IN
        SELECT p.oid, p.proname, pg_get_function_identity_arguments(p.oid) AS args, p.prosrc
        FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        JOIN pg_roles r ON r.oid = p.proowner
        WHERE n.nspname = p_schema
          AND r.rolname = 'eurobase_migrator'
          AND p.proname <> ALL (ARRAY['auth_uid', 'auth_role', 'auth_email'])
    LOOP
        v_src := COALESCE(v_rel.prosrc, '');
        -- Unsafe if it names a platform table / privileged catalog, or any
        -- tenant_ schema other than its own.
        IF v_src ~* v_platform_refs
           OR EXISTS (
               SELECT 1 FROM regexp_matches(v_src, '(tenant_[0-9a-f]{8}_[0-9a-f_]+)', 'gi') AS m(x)
               WHERE m.x[1] <> p_schema)
        THEN
            v_left := v_left + 1;
            CONTINUE;
        END IF;
        BEGIN
            EXECUTE format('ALTER FUNCTION %I.%I(%s) OWNER TO %I', p_schema, v_rel.proname, v_rel.args, v_ddl_role);
            v_reassigned := v_reassigned + 1;
        EXCEPTION WHEN OTHERS THEN
            v_left := v_left + 1;
            RAISE WARNING 'converge_tenant_functions: could not reassign %.%(%): %', p_schema, v_rel.proname, v_rel.args, SQLERRM;
        END;
    END LOOP;

    -- 3. Reassign migrator-owned VIEWS / MATERIALIZED VIEWS whose definition
    --    stays in-schema (same gate; a view that reads platform tables must
    --    keep running as its current owner).
    FOR v_rel IN
        SELECT c.relname, pg_get_viewdef(c.oid) AS def
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles r ON r.oid = c.relowner
        WHERE n.nspname = p_schema AND c.relkind IN ('v', 'm') AND r.rolname = 'eurobase_migrator'
    LOOP
        v_src := COALESCE(v_rel.def, '');
        IF v_src ~* v_platform_refs
           OR EXISTS (SELECT 1 FROM regexp_matches(v_src, '(tenant_[0-9a-f]{8}_[0-9a-f_]+)', 'gi') AS m(x) WHERE m.x[1] <> p_schema)
        THEN
            v_left := v_left + 1;
            CONTINUE;
        END IF;
        BEGIN
            EXECUTE format('ALTER VIEW %I.%I OWNER TO %I', p_schema, v_rel.relname, v_ddl_role);
            v_reassigned := v_reassigned + 1;
        EXCEPTION WHEN OTHERS THEN
            v_left := v_left + 1;
            RAISE WARNING 'converge_tenant_functions: could not reassign view %.%: %', p_schema, v_rel.relname, SQLERRM;
        END;
    END LOOP;

    -- 4. Reassign migrator-owned standalone TYPES (composite/enum/domain).
    --    No body; always in-schema. (Table rowtypes are excluded.)
    FOR v_rel IN
        SELECT t.typname
        FROM pg_type t
        JOIN pg_namespace n ON n.oid = t.typnamespace
        JOIN pg_roles r ON r.oid = t.typowner
        WHERE n.nspname = p_schema AND t.typtype IN ('c', 'e', 'd') AND r.rolname = 'eurobase_migrator'
          AND NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.reltype = t.oid AND c.relkind <> 'c')
    LOOP
        BEGIN
            EXECUTE format('ALTER TYPE %I.%I OWNER TO %I', p_schema, v_rel.typname, v_ddl_role);
            v_reassigned := v_reassigned + 1;
        EXCEPTION WHEN OTHERS THEN
            v_left := v_left + 1;
            RAISE WARNING 'converge_tenant_functions: could not reassign type %.%: %', p_schema, v_rel.typname, SQLERRM;
        END;
    END LOOP;

    IF v_left > 0 THEN
        RAISE WARNING 'converge_tenant_functions %: reassigned %, left migrator-owned % (reach outside their schema or could not be reassigned)',
            p_schema, v_reassigned, v_left;
    END IF;
END;
$conv$;

ALTER FUNCTION public.converge_tenant_functions(TEXT) OWNER TO eurobase_migrator;
REVOKE EXECUTE ON FUNCTION public.converge_tenant_functions(TEXT) FROM PUBLIC;

-- Backfill every tenant schema: first the table convergence (also catches any
-- migrator-owned tables created since 000136), then the function convergence.
DO $backfill$
DECLARE
    v_schema TEXT;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        PERFORM public.converge_tenant_ownership(v_schema);
        PERFORM public.converge_tenant_functions(v_schema);
    END LOOP;
END;
$backfill$;
