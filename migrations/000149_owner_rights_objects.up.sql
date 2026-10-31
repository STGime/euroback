-- Catalog access for platform roles only (database side).
--
-- On the shared cluster every tenant schema lives in one database. The
-- superuser step scripts/db/catalog-lockdown.sql limits the system
-- catalogs to platform roles; tenant roles see their own objects through
-- information_schema. This migration prepares for it:
--
-- 1. public.owner_rights_objects(schema): the catalog read of the
--    owner-rights guard (internal/query/owner_rights_guard.go), which runs
--    inside the tenant's own <schema>_ddl transaction. SECURITY DEFINER
--    (owner eurobase_migrator), answers only the caller's own schema:
--    session_user must be <schema>_ddl, or a platform role (member of
--    eurobase_migrator, or a superuser). session_user, not current_user:
--    tenant SQL can't change it.
-- 2. provision_tenant_ddl_role: grants EXECUTE on it, and USAGE on
--    information_schema, to the tenant's roles directly (they stay members
--    of nothing). Backfilled below.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

CREATE OR REPLACE FUNCTION public.owner_rights_objects(p_schema text)
RETURNS TABLE (kind text, key text, name text, options text[])
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $f$
#variable_conflict use_column
BEGIN
    IF NOT (session_user::text = p_schema || '_ddl'
            OR pg_has_role(session_user, 'eurobase_migrator', 'MEMBER')
            OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = session_user AND r.rolsuper)) THEN
        RAISE EXCEPTION 'owner_rights_objects: not allowed for this schema'
            USING ERRCODE = '42501';
    END IF;
    RETURN QUERY
        WITH ns AS (SELECT n.oid FROM pg_namespace n WHERE n.nspname = p_schema)
        SELECT CASE c.relkind WHEN 'm' THEN 'materialized' ELSE 'view' END,
               c.oid::text || ':' || md5(coalesce((
                   SELECT r.ev_action::text FROM pg_rewrite r
                    WHERE r.ev_class = c.oid AND r.rulename = '_RETURN'), '')),
               c.relname::text, coalesce(c.reloptions, '{}'::text[])
          FROM pg_class c
         WHERE c.relnamespace = (SELECT oid FROM ns) AND c.relkind IN ('v', 'm')
        UNION ALL
        SELECT 'definer', p.oid::text || ':' || md5(pg_get_functiondef(p.oid)),
               p.proname::text, '{}'::text[]
          FROM pg_proc p
         WHERE p.pronamespace = (SELECT oid FROM ns) AND p.prosecdef AND p.prokind IN ('f', 'p')
        UNION ALL
        SELECT 'rule', r.oid::text || ':' || md5(r.ev_action::text || coalesce(r.ev_qual::text, '')
                   || r.ev_type::text || r.is_instead::text),
               (r.rulename || ' on ' || c.relname)::text, '{}'::text[]
          FROM pg_rewrite r
          JOIN pg_class c ON c.oid = r.ev_class
         WHERE c.relnamespace = (SELECT oid FROM ns) AND r.rulename <> '_RETURN'
           AND r.ev_enabled <> 'D';
END;
$f$;

ALTER FUNCTION public.owner_rights_objects(text) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.owner_rights_objects(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.owner_rights_objects(text) TO eurobase_developer;

-- provision_tenant_ddl_role: 000063 body + the grants above for new tenants.
CREATE OR REPLACE FUNCTION public.provision_tenant_ddl_role(p_schema text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $ddl$
DECLARE
    v_ddl_role TEXT := p_schema || '_ddl';
    v_func_role TEXT := p_schema || '_func';
    v_tbl TEXT;
    v_system_tables TEXT[] := ARRAY[
        'users', 'user_identities', 'refresh_tokens', 'email_tokens',
        'storage_objects', 'vault_secrets'
    ];
BEGIN
    -- Created NOLOGIN; the gateway promotes to LOGIN + a derived password
    -- per apply (migrator has ADMIN OPTION below, plus CREATEROLE).
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_ddl_role) THEN
        EXECUTE format('CREATE ROLE %I NOLOGIN', v_ddl_role);
    END IF;

    -- The gateway connects AS this role to run migrations, so it needs
    -- CONNECT (PUBLIC's default CONNECT is revoked on this database; every
    -- other login role is granted it explicitly). Best-effort here: if
    -- eurobase_migrator does not own the database this GRANT is a silent
    -- no-op (a WARNING, not an error), so the AUTHORITATIVE check is at
    -- apply time — MigrationExecutor re-grants and verifies CONNECT with
    -- has_database_privilege and fails loud if migrator can't grant it.
    -- (Ops: make eurobase_migrator own the eurobase database, or grant it
    -- CONNECT … WITH GRANT OPTION, for tenant migrations to work.)
    EXECUTE 'GRANT CONNECT ON DATABASE eurobase TO ' || quote_ident(v_ddl_role);

    -- Own-schema DDL only; public = helper execution, never table access.
    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO %I', p_schema, v_ddl_role);
    EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', v_ddl_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.is_service_role() TO %I', v_ddl_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.current_end_user_id() TO %I', v_ddl_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.is_internal_auth_path() TO %I', v_ddl_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.uuid_generate_v4() TO %I', v_ddl_role);
    -- Bookkeeping only via the session_user-bound helpers — no direct grant
    -- on public.tenant_migrations (so a body cannot forge/read other rows).
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.tenant_migration_checksum(bigint) TO %I', v_ddl_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.record_tenant_migration(bigint, text, text, text) TO %I', v_ddl_role);

    -- Catalog lockdown (000149): the owner-rights guard reads the catalog
    -- through public.owner_rights_objects (own schema only), and the
    -- tenant's roles get information_schema directly (its views show only
    -- objects the role has privileges on). Before the superuser lockdown
    -- step gives eurobase_migrator the grant option on information_schema,
    -- that GRANT is a no-op (PUBLIC still has it).
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.owner_rights_objects(text) TO %I', v_ddl_role);
    EXECUTE format('GRANT USAGE ON SCHEMA information_schema TO %I', v_ddl_role);
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_func_role) THEN
        EXECUTE format('GRANT USAGE ON SCHEMA information_schema TO %I', v_func_role);
    END IF;

    -- Migrator manages ddl-owned objects (console DDL) and sets the role's
    -- login password per apply. Both membership options are granted
    -- explicitly (PG16), because production's eurobase_migrator is NOINHERIT
    -- and managed-PG defaults the unspecified option to FALSE:
    --   INHERIT TRUE → ALTER DEFAULT PRIVILEGES FOR ROLE <ddl> below needs
    --     has_privs_of_role (INHERIT-based) membership of <ddl>;
    --   SET TRUE     → the ALTER TABLE … OWNER TO <ddl> reassign below needs
    --     SET-ROLE-able membership of <ddl> (PG16 owner-reassign rule).
    -- (SET ROLE itself is not an option — forbidden inside a SECURITY
    -- DEFINER function. Plain membership without these fails both checks.)
    EXECUTE format('GRANT %I TO eurobase_migrator WITH INHERIT TRUE, SET TRUE', v_ddl_role);

    -- Tables the ddl role creates become usable at runtime.
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', v_ddl_role, p_schema);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO eurobase_gateway', v_ddl_role, p_schema);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', v_ddl_role, p_schema, v_func_role);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO %I', v_ddl_role, p_schema, v_func_role);

    -- Reassign existing APPLICATION tables (everything except platform
    -- system tables) from migrator to the ddl role. Idempotent.
    FOR v_tbl IN
        SELECT c.relname FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles r ON r.oid = c.relowner
        WHERE n.nspname = p_schema AND c.relkind = 'r'
          AND r.rolname = 'eurobase_migrator'
          AND NOT (c.relname = ANY(v_system_tables))
    LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', p_schema, v_tbl, v_ddl_role);
    END LOOP;
END;
$ddl$;

ALTER FUNCTION public.provision_tenant_ddl_role(text) OWNER TO eurobase_migrator;
REVOKE EXECUTE ON FUNCTION public.provision_tenant_ddl_role(text) FROM PUBLIC;

-- Backfill every existing tenant.
DO $backfill$
DECLARE v_schema TEXT;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_schema || '_ddl') THEN
            EXECUTE format('GRANT EXECUTE ON FUNCTION public.owner_rights_objects(text) TO %I', v_schema || '_ddl');
        END IF;
    END LOOP;
END$backfill$;
