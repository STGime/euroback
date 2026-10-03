-- 000136_converge_tenant_ownership.up.sql
--
-- Groundwork for running customer SQL on a per-project login (the model
-- edge functions and cron use): give the per-project `<schema>_func` role
-- the DML it needs on every application table in a tenant schema, and give
-- `<schema>_ddl` ownership of the migrator-created application tables.
--
-- DELIBERATELY CONSERVATIVE for safe deployment on a live fleet:
--   * Only MIGRATOR-owned application tables are reassigned to `_ddl`.
--     provision_tenant_ddl_role (000063) already does this for `relkind='r'`
--     on every `migrations up`; this migration is the one-shot companion
--     and extends nothing risky.
--   * Gateway- and developer-owned tables are NOT reassigned here. On the
--     shared cluster `eurobase_gateway` is a non-owner on purpose so RLS is
--     enforced for SDK traffic; flipping a gateway-owned table's owner would
--     change RLS enforcement for LIVE SDK traffic. Those objects (legacy
--     SDK/MCP DDL, and tables a pre-phase-1b function body created on the
--     gateway role) are only counted + warned here and converged later, in
--     the step that also moves SDK traffic off the gateway login and settles
--     each table's service policy.
--   * `_func` is GRANTED DML on every application table regardless of owner,
--     so it can serve customer SQL under RLS once a later step cuts the SDK
--     path over to it. Granting `_func` (a non-owner) changes no current
--     behaviour; RLS still decides.
--
-- System tables (users, user_identities, refresh_tokens, email_tokens,
-- storage_objects, storage_shared_prefixes, vault_secrets) stay
-- migrator-owned. storage_shared_prefixes is reasserted to migrator here:
-- 000063's provision_tenant_ddl_role predates it (000130) and its stale
-- list let it drift to `_ddl` on newer tenants.
--
-- A short lock_timeout keeps the one-shot backfill from stalling live
-- traffic: an object whose ACCESS EXCLUSIVE lock can't be taken quickly is
-- skipped (counted + warned), not blocked on, and can be re-run later.
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

CREATE OR REPLACE FUNCTION public.converge_tenant_ownership(p_schema TEXT)
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
        'storage_objects', 'storage_shared_prefixes', 'vault_secrets'
    ];
    v_rel        RECORD;
    v_reassigned INT := 0;
    v_skipped    INT := 0;
    v_gw_left    INT := 0;
    v_dev_left   INT := 0;
    v_gw_rls     INT := 0;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_ddl_role) THEN
        RAISE WARNING 'converge_tenant_ownership: role % missing, skipping %', v_ddl_role, p_schema;
        RETURN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_func_role) THEN
        RAISE WARNING 'converge_tenant_ownership: role % missing, skipping %', v_func_role, p_schema;
        RETURN;
    END IF;

    -- Don't wait behind live queries; skip a contended object instead.
    SET LOCAL lock_timeout = '3s';

    -- 1. Reassign MIGRATOR-owned, non-system application tables to _ddl.
    --    (Owned sequences follow automatically.) Each in a subtransaction
    --    so a lock timeout or any error on one table is skipped, not fatal.
    FOR v_rel IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles r ON r.oid = c.relowner
        WHERE n.nspname = p_schema
          AND c.relkind IN ('r', 'p')
          AND r.rolname = 'eurobase_migrator'
          AND NOT (c.relname = ANY(v_system_tables))
    LOOP
        BEGIN
            EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', p_schema, v_rel.relname, v_ddl_role);
            v_reassigned := v_reassigned + 1;
        EXCEPTION WHEN OTHERS THEN
            v_skipped := v_skipped + 1;
            RAISE WARNING 'converge_tenant_ownership: could not reassign %.%: %', p_schema, v_rel.relname, SQLERRM;
        END;
    END LOOP;

    -- 2. Keep storage_shared_prefixes migrator-owned (it may have drifted to
    --    _ddl via provision_tenant_ddl_role's stale list). Best-effort.
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles r ON r.oid = c.relowner
        WHERE n.nspname = p_schema AND c.relname = 'storage_shared_prefixes'
          AND c.relkind = 'r' AND r.rolname = v_ddl_role
    ) THEN
        BEGIN
            EXECUTE format('ALTER TABLE %I.storage_shared_prefixes OWNER TO eurobase_migrator', p_schema);
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'converge_tenant_ownership: could not restore %.storage_shared_prefixes to migrator: %', p_schema, SQLERRM;
        END;
    END IF;

    -- 3. Grant _func the DML it needs on every non-system application table
    --    and sequence, whatever the owner. _func is a non-owner, so RLS
    --    still applies; this only readies it to serve customer SQL once the
    --    SDK path moves onto it. Idempotent.
    FOR v_rel IN
        SELECT c.relname, c.relkind
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = p_schema
          AND c.relkind IN ('r', 'p', 'S')
          AND NOT (c.relkind IN ('r', 'p') AND c.relname = ANY(v_system_tables))
          AND NOT (c.relkind = 'S' AND EXISTS (
              SELECT 1 FROM pg_depend d
              JOIN pg_class t ON t.oid = d.refobjid AND d.refclassid = 'pg_class'::regclass
              WHERE d.objid = c.oid AND d.deptype IN ('a', 'i')
                AND t.relname = ANY(v_system_tables)
          ))
    LOOP
        BEGIN
            IF v_rel.relkind = 'S' THEN
                EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %I.%I TO %I', p_schema, v_rel.relname, v_func_role);
            ELSE
                EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I.%I TO %I', p_schema, v_rel.relname, v_func_role);
            END IF;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'converge_tenant_ownership: grant to % on %.% failed: %', v_func_role, p_schema, v_rel.relname, SQLERRM;
        END;
    END LOOP;

    -- 4. Inventory the objects a later, coordinated step must converge:
    --    gateway- and developer-owned application tables, and how many of the
    --    gateway-owned ones have RLS enabled (those are where reassigning the
    --    owner would change RLS enforcement for live SDK traffic).
    SELECT
        count(*) FILTER (WHERE r.rolname = 'eurobase_gateway'),
        count(*) FILTER (WHERE r.rolname = 'eurobase_developer'),
        count(*) FILTER (WHERE r.rolname = 'eurobase_gateway' AND c.relrowsecurity)
    INTO v_gw_left, v_dev_left, v_gw_rls
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_roles r ON r.oid = c.relowner
    WHERE n.nspname = p_schema
      AND c.relkind IN ('r', 'p')
      AND r.rolname IN ('eurobase_gateway', 'eurobase_developer')
      AND NOT (c.relname = ANY(v_system_tables));

    IF v_skipped > 0 OR v_gw_left > 0 OR v_dev_left > 0 THEN
        RAISE WARNING 'converge_tenant_ownership %: reassigned %, skipped %, deferred gateway-owned % (% with RLS), developer-owned %',
            p_schema, v_reassigned, v_skipped, v_gw_left, v_gw_rls, v_dev_left;
    END IF;
END;
$conv$;

ALTER FUNCTION public.converge_tenant_ownership(TEXT) OWNER TO eurobase_migrator;
-- Privileged helper: migrator context only (000064 convention).
REVOKE EXECUTE ON FUNCTION public.converge_tenant_ownership(TEXT) FROM PUBLIC;

-- Backfill every existing tenant schema. (Does NOT call
-- provision_tenant_ddl_role: the roles already exist for every tenant, and
-- that helper's stale system-table list would re-drift storage_shared_prefixes.)
DO $backfill$
DECLARE
    v_schema TEXT;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        PERFORM public.converge_tenant_ownership(v_schema);
    END LOOP;
END;
$backfill$;
