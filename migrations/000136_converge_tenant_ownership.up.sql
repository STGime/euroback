-- 000136_converge_tenant_ownership.up.sql
--
-- One owner per project: give every application table / sequence / view /
-- routine in a tenant schema to that schema's `<schema>_ddl` role, whatever
-- role created it (migrator, the gateway or developer runtime roles, or
-- _ddl already). Step 1 of moving every customer-SQL path onto a
-- per-project login: once a single per-project role owns the application
-- objects, that role (and its _func sibling) can run customer DDL and DML
-- without a shared platform login.
--
-- provision_tenant_ddl_role (000063) already reassigns migrator-owned
-- application tables. This widens it to gateway- and developer-owned ones
-- (legacy SDK / MCP DDL, and objects a function body created on the gateway
-- role), to sequences, views and routines, and re-grants the runtime roles
-- the DML they need on every reassigned object.
--
-- Ownership reassign requires the migration role (eurobase_migrator) to be
-- a member of each object's current owner. Migrator is a member of _ddl
-- (set TRUE) and of eurobase_gateway, so migrator- and gateway-owned
-- objects reassign. It is NOT a member of eurobase_developer (that
-- membership runs the other way, and the reverse would be a cycle), so a
-- developer-owned object cannot be reassigned here — each reassign is
-- wrapped so such a case is logged (WARNING) and the migration still
-- applies; a leftover is counted at the end for an ops follow-up (a
-- superuser REASSIGN OWNED BY eurobase_developer, if prod has any).
--
-- System tables (users, user_identities, refresh_tokens, email_tokens,
-- storage_objects, storage_shared_prefixes, vault_secrets) stay
-- migrator-owned: the platform manages them.
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
    v_rel     RECORD;
    v_stmt    TEXT;
    v_left    INT := 0;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_ddl_role) THEN
        RAISE EXCEPTION 'converge_tenant_ownership: role % does not exist (run provision_tenant_ddl_role first)', v_ddl_role;
    END IF;

    -- Reassign application relations (table r, partitioned p, sequence S,
    -- view v, materialized view m) owned by a platform role to _ddl. Skip
    -- system tables and the sequences they own (identity/serial: pg_depend
    -- deptype a auto or i internal).
    FOR v_rel IN
        SELECT c.relname, c.relkind
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles r ON r.oid = c.relowner
        WHERE n.nspname = p_schema
          AND c.relkind IN ('r', 'p', 'S', 'v', 'm')
          AND r.rolname IN ('eurobase_migrator', 'eurobase_gateway', 'eurobase_developer')
          AND NOT (c.relkind IN ('r', 'p') AND c.relname = ANY(v_system_tables))
          AND NOT (c.relkind = 'S' AND EXISTS (
              SELECT 1 FROM pg_depend d
              JOIN pg_class t ON t.oid = d.refobjid
              WHERE d.objid = c.oid AND d.deptype IN ('a', 'i')
                AND t.relname = ANY(v_system_tables)
          ))
    LOOP
        v_stmt := CASE v_rel.relkind
            WHEN 'S' THEN format('ALTER SEQUENCE %I.%I OWNER TO %I', p_schema, v_rel.relname, v_ddl_role)
            WHEN 'v' THEN format('ALTER VIEW %I.%I OWNER TO %I', p_schema, v_rel.relname, v_ddl_role)
            WHEN 'm' THEN format('ALTER MATERIALIZED VIEW %I.%I OWNER TO %I', p_schema, v_rel.relname, v_ddl_role)
            ELSE format('ALTER TABLE %I.%I OWNER TO %I', p_schema, v_rel.relname, v_ddl_role)
        END;
        BEGIN
            EXECUTE v_stmt;
        EXCEPTION WHEN OTHERS THEN
            v_left := v_left + 1;
            RAISE WARNING 'converge_tenant_ownership: could not reassign %.% (%): %',
                p_schema, v_rel.relname, v_rel.relkind, SQLERRM;
        END;
    END LOOP;

    -- Application routines a customer defined via the SQL editor.
    FOR v_rel IN
        SELECT p.oid::regprocedure::text AS sig
        FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        JOIN pg_roles r ON r.oid = p.proowner
        WHERE n.nspname = p_schema
          AND r.rolname IN ('eurobase_migrator', 'eurobase_gateway', 'eurobase_developer')
    LOOP
        BEGIN
            EXECUTE format('ALTER ROUTINE %s OWNER TO %I', v_rel.sig, v_ddl_role);
        EXCEPTION WHEN OTHERS THEN
            v_left := v_left + 1;
            RAISE WARNING 'converge_tenant_ownership: could not reassign routine % : %', v_rel.sig, SQLERRM;
        END;
    END LOOP;

    -- Re-grant runtime DML on the now-_ddl-owned application objects.
    -- Reassigning an owner keeps existing grants, but gateway/developer-
    -- created objects never granted the func role. System tables excluded,
    -- so this never widens access to users / vault_secrets / etc.
    FOR v_rel IN
        SELECT c.relname, c.relkind
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles r ON r.oid = c.relowner
        WHERE n.nspname = p_schema
          AND r.rolname = v_ddl_role
          AND c.relkind IN ('r', 'p', 'S', 'v', 'm')
          AND NOT (c.relkind IN ('r', 'p') AND c.relname = ANY(v_system_tables))
    LOOP
        IF v_rel.relkind = 'S' THEN
            EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %I.%I TO eurobase_gateway', p_schema, v_rel.relname);
            EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %I.%I TO %I', p_schema, v_rel.relname, v_func_role);
        ELSE
            EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I.%I TO eurobase_gateway', p_schema, v_rel.relname);
            EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I.%I TO %I', p_schema, v_rel.relname, v_func_role);
        END IF;
    END LOOP;

    IF v_left > 0 THEN
        RAISE WARNING 'converge_tenant_ownership: % object(s) in % left non-_ddl-owned (likely developer-owned; needs a superuser REASSIGN OWNED BY)', v_left, p_schema;
    END IF;
END;
$conv$;

ALTER FUNCTION public.converge_tenant_ownership(TEXT) OWNER TO eurobase_migrator;
-- Privileged helper: migrator context only (000064 convention).
REVOKE EXECUTE ON FUNCTION public.converge_tenant_ownership(TEXT) FROM PUBLIC;

-- Backfill every existing tenant schema.
DO $backfill$
DECLARE
    v_schema TEXT;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        PERFORM public.provision_tenant_ddl_role(v_schema);
        PERFORM public.converge_tenant_ownership(v_schema);
    END LOOP;
END;
$backfill$;
