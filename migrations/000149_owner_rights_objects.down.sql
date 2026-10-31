-- Reverts 000149: provision_tenant_ddl_role back to its 000063 body, and
-- drops public.owner_rights_objects (the guard then reads the catalog
-- directly again, which needs the superuser lockdown step reverted first).

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

DROP FUNCTION IF EXISTS public.owner_rights_objects(text);
