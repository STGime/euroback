-- Tenant migration containment (#190, PR #209 review, v2): run each tenant
-- migration under a PER-TENANT LOGIN role the gateway connects as directly
-- — never as a shared role that can reach more than one tenant.
--
-- v1 (a shared eurobase_ddl_runner that was a member of every
-- tenant_<id>_ddl) was vulnerable to a cross-tenant pivot: a migration
-- body could RESET ROLE back to the shared login role and then SET ROLE
-- into another tenant's ddl role. Verified on Postgres 16. The only
-- containment that holds against RESET ROLE in an arbitrary body is a
-- session/login role that is a member of exactly one tenant — i.e. the
-- per-tenant role itself is the login role. RESET ROLE then lands on that
-- same role (a member of nothing), and SET ROLE into another tenant is
-- denied by Postgres.
--
-- Per tenant: tenant_<id>_ddl — owns the tenant's application tables,
-- CREATE on its own schema, USAGE+EXECUTE on the public RLS helpers,
-- member of NOTHING. The gateway promotes it to LOGIN and sets a derived
-- password per apply (migrator holds CREATEROLE + ADMIN OPTION on it), then
-- opens a short-lived connection AS that role to run the migration.
--
-- Bookkeeping is forgery-proof and isolating: tenant roles have NO direct
-- grant on public.tenant_migrations. Two SECURITY DEFINER helpers, bound
-- to session_user (which a body cannot change without superuser), let a
-- role read/write only its OWN project's history:
--   public.tenant_migration_checksum(version) -> the role's recorded checksum
--   public.record_tenant_migration(version,name,sql,checksum) -> insert for
--     the role's own project.

-- ── 1. session_user-bound bookkeeping helpers ──
-- session_user is tenant_<id>_ddl; the schema is that minus the _ddl suffix.
CREATE OR REPLACE FUNCTION public.tenant_migration_checksum(p_version bigint)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $tmc$
DECLARE v_schema text; v_project uuid; v_cs text;
BEGIN
    IF right(session_user, 4) <> '_ddl' THEN
        RAISE EXCEPTION 'not a tenant ddl role: %', session_user;
    END IF;
    v_schema := left(session_user, length(session_user) - 4);
    SELECT id INTO v_project FROM public.projects WHERE schema_name = v_schema;
    IF v_project IS NULL THEN RAISE EXCEPTION 'no project for schema %', v_schema; END IF;
    SELECT checksum INTO v_cs FROM public.tenant_migrations
     WHERE project_id = v_project AND version = p_version;
    RETURN v_cs;
END$tmc$;
ALTER FUNCTION public.tenant_migration_checksum(bigint) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.tenant_migration_checksum(bigint) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.record_tenant_migration(p_version bigint, p_name text, p_sql text, p_checksum text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $rtm$
DECLARE v_schema text; v_project uuid;
BEGIN
    IF right(session_user, 4) <> '_ddl' THEN
        RAISE EXCEPTION 'not a tenant ddl role: %', session_user;
    END IF;
    v_schema := left(session_user, length(session_user) - 4);
    SELECT id INTO v_project FROM public.projects WHERE schema_name = v_schema;
    IF v_project IS NULL THEN RAISE EXCEPTION 'no project for schema %', v_schema; END IF;
    INSERT INTO public.tenant_migrations (project_id, version, name, sql, checksum, applied_by)
    VALUES (v_project, p_version, p_name, p_sql, p_checksum, session_user);
END$rtm$;
ALTER FUNCTION public.record_tenant_migration(bigint, text, text, text) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.record_tenant_migration(bigint, text, text, text) FROM PUBLIC;

-- ── 2. Per-tenant DDL role provisioning helper ──
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
    -- login password per apply. The membership is granted WITH INHERIT TRUE
    -- so the next statement works even though eurobase_migrator is NOINHERIT
    -- in production: ALTER DEFAULT PRIVILEGES FOR ROLE <ddl> needs
    -- has_privs_of_role (INHERIT-based) membership of <ddl>. Per-membership
    -- INHERIT TRUE (PG16) overrides migrator's role-level NOINHERIT for just
    -- this grant. (SET ROLE is not an option — it's forbidden inside a
    -- SECURITY DEFINER function; plain membership fails the FOR ROLE check.)
    EXECUTE format('GRANT %I TO eurobase_migrator WITH INHERIT TRUE', v_ddl_role);

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

-- ── 3. Backfill every existing tenant ──
DO $backfill$
DECLARE v_schema TEXT;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        PERFORM public.provision_tenant_ddl_role(v_schema);
        RAISE NOTICE 'provisioned ddl role for %', v_schema;
    END LOOP;
END$backfill$;

-- ── 4. provision_tenant: 000060 body + ddl-role call for new tenants ──
CREATE OR REPLACE FUNCTION public.provision_tenant(
    p_project_id   UUID,
    p_display_name TEXT,
    p_plan         TEXT DEFAULT 'free'
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
AS $fn$
DECLARE
    v_schema_name TEXT;
    v_func_role   TEXT;
    v_ddl_role    TEXT;
BEGIN
    v_schema_name := 'tenant_' || replace(p_project_id::text, '-', '_');
    v_func_role   := v_schema_name || '_func';

    EXECUTE format('CREATE SCHEMA %I', v_schema_name);
    EXECUTE format('SET search_path TO %I', v_schema_name);

    EXECUTE format(
        'CREATE TABLE %I.users (
            id                 UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            email              TEXT        UNIQUE,
            phone              TEXT,
            password_hash      TEXT,
            display_name       TEXT,
            avatar_url         TEXT,
            metadata           JSONB       DEFAULT ''{}''::jsonb,
            provider           TEXT        DEFAULT ''email'',
            provider_user_id   TEXT,
            email_confirmed_at TIMESTAMPTZ,
            phone_confirmed_at TIMESTAMPTZ,
            last_sign_in_at    TIMESTAMPTZ,
            banned_at          TIMESTAMPTZ,
            created_at         TIMESTAMPTZ DEFAULT now(),
            updated_at         TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );
    EXECUTE format('CREATE UNIQUE INDEX idx_users_provider ON %I.users(provider, provider_user_id) WHERE provider_user_id IS NOT NULL', v_schema_name);
    EXECUTE format('CREATE UNIQUE INDEX idx_users_phone ON %I.users(phone) WHERE phone IS NOT NULL', v_schema_name);

    EXECUTE format(
        'CREATE TABLE %I.user_identities (
            id               UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            user_id          UUID        NOT NULL REFERENCES %I.users(id) ON DELETE CASCADE,
            provider         TEXT        NOT NULL,
            provider_user_id TEXT        NOT NULL,
            identity_data    JSONB       DEFAULT ''{}''::jsonb,
            last_sign_in_at  TIMESTAMPTZ,
            created_at       TIMESTAMPTZ DEFAULT now(),
            updated_at       TIMESTAMPTZ DEFAULT now(),
            UNIQUE(provider, provider_user_id)
        )',
        v_schema_name, v_schema_name
    );
    EXECUTE format('CREATE INDEX idx_user_identities_user_id ON %I.user_identities(user_id)', v_schema_name);

    EXECUTE format(
        'CREATE TABLE %I.refresh_tokens (
            id         UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            user_id    UUID        NOT NULL REFERENCES %I.users(id) ON DELETE CASCADE,
            token_hash TEXT        NOT NULL,
            expires_at TIMESTAMPTZ NOT NULL,
            revoked_at TIMESTAMPTZ,
            created_at TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name, v_schema_name
    );
    EXECUTE format('CREATE INDEX idx_refresh_tokens_token_hash ON %I.refresh_tokens(token_hash)', v_schema_name);
    EXECUTE format('CREATE INDEX idx_refresh_tokens_user_id ON %I.refresh_tokens(user_id)', v_schema_name);

    EXECUTE format(
        'CREATE TABLE %I.email_tokens (
            id          UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            user_id     UUID        NOT NULL REFERENCES %I.users(id) ON DELETE CASCADE,
            token_hash  TEXT        NOT NULL,
            token_type  TEXT        NOT NULL CHECK (token_type IN (''verification'',''password_reset'',''magic_link'',''phone_verification'')),
            expires_at  TIMESTAMPTZ NOT NULL,
            used_at     TIMESTAMPTZ,
            created_at  TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name, v_schema_name
    );
    EXECUTE format('CREATE INDEX idx_email_tokens_hash ON %I.email_tokens(token_hash)', v_schema_name);

    EXECUTE format(
        'CREATE TABLE %I.storage_objects (
            id            UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            key           TEXT        NOT NULL UNIQUE,
            content_type  TEXT,
            size_bytes    BIGINT,
            uploaded_by   UUID        REFERENCES %I.users(id),
            metadata      JSONB       DEFAULT ''{}''::jsonb,
            created_at    TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name, v_schema_name
    );

    EXECUTE format(
        'CREATE TABLE %I.todos (
            id         UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            title      TEXT        NOT NULL,
            completed  BOOLEAN     DEFAULT false,
            created_at TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );
    EXECUTE format(
        'INSERT INTO %I.todos (title, completed) VALUES
            (''Learn about Eurobase'', true),
            (''Build my first EU-sovereign app'', false),
            (''Deploy to production'', false)',
        v_schema_name
    );

    -- vault_secrets: secret/nonce (correct, restored by 000056) plus the
    -- NEW key_version column (this migration). Existing-tenant rows are
    -- backfilled to 0 above; new tenants start every row at the column
    -- default 0 until the first write seals it at the current version.
    EXECUTE format(
        'CREATE TABLE %I.vault_secrets (
            id          UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            name        TEXT        UNIQUE NOT NULL,
            secret      BYTEA       NOT NULL,
            nonce       BYTEA       NOT NULL,
            key_version smallint    NOT NULL DEFAULT 0,
            description TEXT        DEFAULT '''',
            created_at  TIMESTAMPTZ DEFAULT now(),
            updated_at  TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );

    EXECUTE format('ALTER TABLE %I.users ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.user_identities ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.refresh_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.email_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.vault_secrets ENABLE ROW LEVEL SECURITY', v_schema_name);

    EXECUTE format(
        'CREATE POLICY user_self_access ON %I.users
         USING (public.is_service_role() OR id = public.current_end_user_id())
         WITH CHECK (public.is_service_role() OR id = public.current_end_user_id())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY user_identities_policy ON %I.user_identities
         USING (public.is_service_role() OR user_id = public.current_end_user_id())
         WITH CHECK (public.is_service_role() OR user_id = public.current_end_user_id())',
        v_schema_name
    );
    -- Sensitive tables stay on the stricter intent GUC (from 000055/#164).
    EXECUTE format(
        'CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens
         USING (public.is_internal_auth_path())
         WITH CHECK (public.is_internal_auth_path())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY email_tokens_policy ON %I.email_tokens
         USING (public.is_internal_auth_path())
         WITH CHECK (public.is_internal_auth_path())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY storage_owner_access ON %I.storage_objects
         USING (public.is_service_role() OR uploaded_by = public.current_end_user_id())
         WITH CHECK (public.is_service_role() OR uploaded_by = public.current_end_user_id())',
        v_schema_name
    );
    EXECUTE format('CREATE POLICY public_todos ON %I.todos FOR ALL USING (true)', v_schema_name);
    EXECUTE format(
        'CREATE POLICY vault_secrets_policy ON %I.vault_secrets
         USING (public.is_internal_auth_path())
         WITH CHECK (public.is_internal_auth_path())',
        v_schema_name
    );

    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.auth_uid() RETURNS uuid
         LANGUAGE sql STABLE AS $_$
           SELECT public.current_end_user_id();
         $_$', v_schema_name
    );
    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.auth_role() RETURNS text
         LANGUAGE sql STABLE AS $_$
           SELECT COALESCE(current_setting(''app.end_user_role'', true), ''anon'');
         $_$', v_schema_name
    );
    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.auth_email() RETURNS text
         LANGUAGE sql STABLE AS $_$
           SELECT email FROM %I.users WHERE id = public.current_end_user_id();
         $_$', v_schema_name, v_schema_name
    );

    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', v_schema_name);

    -- ── Per-tenant function-runner role (000047) ──
    EXECUTE format('CREATE ROLE %I NOLOGIN INHERIT', v_func_role);
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', v_schema_name, v_func_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I', v_schema_name, v_func_role);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I', v_schema_name, v_func_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %I TO %I', v_schema_name, v_func_role);
    EXECUTE format(
        'ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I
         GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I',
        v_schema_name, v_func_role
    );
    EXECUTE format(
        'ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I
         GRANT USAGE, SELECT ON SEQUENCES TO %I',
        v_schema_name, v_func_role
    );
    -- Issue #188 follow-up: USAGE on public + EXECUTE on the GUC-reading
    -- helpers, or every preset RLS policy fails at expression init under
    -- SET LOCAL ROLE (the func role does not inherit the runner's grants).
    EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', v_func_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.is_service_role() TO %I', v_func_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.current_end_user_id() TO %I', v_func_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.is_internal_auth_path() TO %I', v_func_role);
    EXECUTE format('GRANT %I TO eurobase_function_runner', v_func_role);

    -- Per-tenant DDL role for tenant migrations (#190 / PR #209).
    v_ddl_role := v_schema_name || '_ddl';
    PERFORM public.provision_tenant_ddl_role(v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos OWNER TO %I', v_schema_name, v_ddl_role);

    UPDATE public.projects SET schema_name = v_schema_name WHERE id = p_project_id;
    SET search_path TO public;
END;
$fn$;

ALTER FUNCTION public.provision_tenant(UUID, TEXT, TEXT) OWNER TO eurobase_migrator;
