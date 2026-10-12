-- 000144_passkey_tables_platform_owned.up.sql
--
-- #784: the end-user passkey tables (`user_passkey_credentials`,
-- `webauthn_challenges`, added in 000137/000138) were never in
-- converge_tenant_ownership's `v_system_tables` exclusion list, so the
-- convergence backfills reassigned them to `<schema>_ddl` like application
-- tables. Every other platform system table is migrator-owned (so a developer
-- can read/write rows under RLS but cannot ALTER/DROP the table). This makes
-- the passkey tables consistent: platform-owned, not `_ddl`-owned.
--
-- Flip-neutral: their RLS policies gate on `is_service_role()` (not the #164
-- `is_internal_auth_path()` lock) and they hold public keys, not secrets, so
-- console/SDK access is unchanged. End-user passkey auth runs on the developer
-- pool (the tables are revoked from eurobase_gateway), which keeps full access
-- as the owner / via INHERIT. `_ddl` loses implicit (owner) access to them and
-- regains explicit DML + REFERENCES on the worker keeper's next pass
-- (DDLEnsurer.grantDDLAccess, ≤5 min) — the same pattern storage_shared_prefixes
-- already relies on.
--
-- Two changes: (1) add the passkey tables to `v_system_tables`, so convergence
-- stops moving them and future provisioning treats them as system tables;
-- (2) generalize step 2 to restore ANY system table that drifted to `_ddl`
-- back to the migrator (was storage_shared_prefixes only). Then backfill.
-- No explicit BEGIN/COMMIT — golang-migrate wraps the file.

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
        'storage_objects', 'storage_shared_prefixes', 'vault_secrets',
        'user_passkey_credentials', 'webauthn_challenges'
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

    -- 2. System tables must stay migrator-owned. Restore any that drifted to
    --    _ddl: storage_shared_prefixes (provision_tenant_ddl_role's stale
    --    list) and the end-user passkey tables (this list predated them, #784).
    --    Best-effort per table.
    FOR v_rel IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles r ON r.oid = c.relowner
        WHERE n.nspname = p_schema AND c.relkind = 'r'
          AND c.relname = ANY(v_system_tables)
          AND r.rolname = v_ddl_role
    LOOP
        BEGIN
            EXECUTE format('ALTER TABLE %I.%I OWNER TO eurobase_migrator', p_schema, v_rel.relname);
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'converge_tenant_ownership: could not restore %.% to migrator: %', p_schema, v_rel.relname, SQLERRM;
        END;
    END LOOP;

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
REVOKE EXECUTE ON FUNCTION public.converge_tenant_ownership(TEXT) FROM PUBLIC;

-- Backfill: restore the passkey tables (and any other drifted system table)
-- to the migrator across every tenant schema.
DO $backfill$
DECLARE
    v_schema TEXT;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        PERFORM public.converge_tenant_ownership(v_schema);
    END LOOP;
END;
$backfill$;
