-- 000141_converge_tenant_functions.up.sql
--
-- Step 6c (continued): move migrator-owned objects in a tenant schema to the
-- project's own `<schema>_ddl` role, so once console SQL runs as `_ddl`
-- (step 6d) a developer can edit their own objects (CREATE OR REPLACE / DROP
-- need ownership).
--
-- RUNTIME-NEUTRAL BY CONSTRUCTION (so nothing a customer runs can change):
-- this migration reassigns ONLY objects whose behaviour does not depend on
-- their owner:
--   * PLAIN (SECURITY INVOKER) functions and procedures — they run with the
--     CALLER's privileges whatever the owner, so reassigning is a no-op at
--     runtime (triggers that call them run as the statement role; RPC runs as
--     `_func`).
--   * Standalone TYPES (composite/enum/domain) — no execution.
--   * Views declared `security_invoker` — rows are fetched as the CALLER.
--
-- It does NOT touch:
--   * SECURITY DEFINER functions/procedures — they run as their OWNER, so
--     reassigning migrator -> _ddl could change RLS-bypass semantics. Those
--     are handled per-object after review (free-tier only in prod today), and
--     via step 6d's just-in-time reassignment when their author next edits
--     them. Left migrator-owned here.
--   * Owner-run (non-invoker) views — same reason.
--   * Tables — `converge_tenant_ownership` (000136) handles those; it is
--     called first in the backfill below to catch any created since.
--
-- lock_timeout bounds contention; each object reassigns in a subtransaction so
-- one failure is skipped, not fatal. golang-migrate wraps the file in one
-- transaction. No explicit BEGIN/COMMIT.

CREATE OR REPLACE FUNCTION public.converge_tenant_functions(p_schema TEXT)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $conv$
DECLARE
    v_ddl_role TEXT := p_schema || '_ddl';
    v_rel      RECORD;
    v_reassigned INT := 0;
    v_left_definer INT := 0;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_ddl_role) THEN
        RAISE WARNING 'converge_tenant_functions: role % missing, skipping %', v_ddl_role, p_schema;
        RETURN;
    END IF;

    SET LOCAL lock_timeout = '3s';

    -- 1. PLAIN functions/procedures/aggregates (prosecdef = false) -> _ddl.
    --    ALTER ROUTINE covers functions, procedures and aggregates. The
    --    platform RLS helpers are SECURITY DEFINER, so they are skipped here
    --    by the prosecdef filter (belt-and-braces: exclude by name too).
    FOR v_rel IN
        SELECT p.proname, pg_get_function_identity_arguments(p.oid) AS args
        FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        JOIN pg_roles r ON r.oid = p.proowner
        WHERE n.nspname = p_schema
          AND r.rolname = 'eurobase_migrator'
          AND p.prosecdef = false
          AND p.proname <> ALL (ARRAY['auth_uid', 'auth_role', 'auth_email'])
    LOOP
        BEGIN
            EXECUTE format('ALTER ROUTINE %I.%I(%s) OWNER TO %I', p_schema, v_rel.proname, v_rel.args, v_ddl_role);
            v_reassigned := v_reassigned + 1;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'converge_tenant_functions: could not reassign routine %.%(%): %', p_schema, v_rel.proname, v_rel.args, SQLERRM;
        END;
    END LOOP;

    -- Count the SECURITY DEFINER functions left migrator-owned (handled
    -- per-object / by step 6d), for visibility.
    SELECT count(*) INTO v_left_definer
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    JOIN pg_roles r ON r.oid = p.proowner
    WHERE n.nspname = p_schema AND r.rolname = 'eurobase_migrator'
      AND p.prosecdef = true AND p.proname <> ALL (ARRAY['auth_uid', 'auth_role', 'auth_email']);

    -- 2. security_invoker views -> _ddl (rows fetched as the caller).
    FOR v_rel IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles r ON r.oid = c.relowner
        WHERE n.nspname = p_schema AND c.relkind = 'v' AND r.rolname = 'eurobase_migrator'
          AND EXISTS (SELECT 1 FROM unnest(COALESCE(c.reloptions, '{}')) o
                      WHERE lower(o) IN ('security_invoker=true', 'security_invoker=on', 'security_invoker=1'))
    LOOP
        BEGIN
            EXECUTE format('ALTER VIEW %I.%I OWNER TO %I', p_schema, v_rel.relname, v_ddl_role);
            v_reassigned := v_reassigned + 1;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'converge_tenant_functions: could not reassign view %.%: %', p_schema, v_rel.relname, SQLERRM;
        END;
    END LOOP;

    -- 3. Standalone types (composite/enum/domain) -> _ddl. Domains need
    --    ALTER DOMAIN; the rowtype of a table is excluded.
    FOR v_rel IN
        SELECT t.typname, t.typtype
        FROM pg_type t
        JOIN pg_namespace n ON n.oid = t.typnamespace
        JOIN pg_roles r ON r.oid = t.typowner
        WHERE n.nspname = p_schema AND t.typtype IN ('c', 'e', 'd') AND r.rolname = 'eurobase_migrator'
          AND NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.reltype = t.oid AND c.relkind <> 'c')
    LOOP
        BEGIN
            IF v_rel.typtype = 'd' THEN
                EXECUTE format('ALTER DOMAIN %I.%I OWNER TO %I', p_schema, v_rel.typname, v_ddl_role);
            ELSE
                EXECUTE format('ALTER TYPE %I.%I OWNER TO %I', p_schema, v_rel.typname, v_ddl_role);
            END IF;
            v_reassigned := v_reassigned + 1;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'converge_tenant_functions: could not reassign type %.%: %', p_schema, v_rel.typname, SQLERRM;
        END;
    END LOOP;

    IF v_left_definer > 0 THEN
        RAISE NOTICE 'converge_tenant_functions %: reassigned %, left % SECURITY DEFINER function(s) migrator-owned (handled per-object / step 6d)',
            p_schema, v_reassigned, v_left_definer;
    END IF;
END;
$conv$;

ALTER FUNCTION public.converge_tenant_functions(TEXT) OWNER TO eurobase_migrator;
REVOKE EXECUTE ON FUNCTION public.converge_tenant_functions(TEXT) FROM PUBLIC;

-- Backfill every tenant schema: table convergence first (catches any
-- migrator-owned tables created since 000136), then the object convergence.
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
