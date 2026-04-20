-- 000038_rls_service_role.up.sql
--
-- Fix the RLS regression introduced by the role split (000037). The gateway
-- used to run as eurobase_api (owner of every tenant table), so ownership
-- bypassed RLS. Now it runs as eurobase_gateway (non-owner), so policies
-- actively evaluate — and the empty-string-to-uuid cast in the existing
-- policies raises an exception whenever the app.end_user_id GUC is unset
-- (platform-admin paths, pre-auth lookups, GDPR export).
--
-- Two fixes: (1) make the policies safe to evaluate with no end-user
-- context, (2) give paths that legitimately lack end-user context an
-- explicit `service` escape hatch. Gateway code sets app.end_user_role
-- to 'service' for those paths; RLS permits when that role is set.

BEGIN;

-- ============================================================================
-- Platform helper functions (reusable from every tenant schema)
-- ============================================================================

CREATE OR REPLACE FUNCTION public.current_end_user_id() RETURNS uuid
    LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.end_user_id', true), '')::uuid
$$;

CREATE OR REPLACE FUNCTION public.is_service_role() RETURNS boolean
    LANGUAGE sql STABLE AS $$
    SELECT current_setting('app.end_user_role', true) = 'service'
$$;

ALTER FUNCTION public.current_end_user_id() OWNER TO eurobase_migrator;
ALTER FUNCTION public.is_service_role() OWNER TO eurobase_migrator;

GRANT EXECUTE ON FUNCTION public.current_end_user_id() TO eurobase_gateway;
GRANT EXECUTE ON FUNCTION public.is_service_role() TO eurobase_gateway;
GRANT EXECUTE ON FUNCTION public.current_end_user_id() TO eurobase_api;
GRANT EXECUTE ON FUNCTION public.is_service_role() TO eurobase_api;

-- ============================================================================
-- Backfill: fix auth_uid() in every tenant schema to handle empty GUC,
-- and rewrite the two restrictive policies to permit service role.
-- ============================================================================

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        -- Safe auth_uid() — NULL when no end-user context, instead of throwing.
        EXECUTE format(
            'CREATE OR REPLACE FUNCTION %I.auth_uid() RETURNS uuid
             LANGUAGE sql STABLE AS $_$
               SELECT public.current_end_user_id();
             $_$', rec.schema_name
        );

        -- Rewrite user_self_access on users: permit service OR own id.
        EXECUTE format('DROP POLICY IF EXISTS user_self_access ON %I.users', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY user_self_access ON %I.users
             USING (public.is_service_role() OR id = public.current_end_user_id())
             WITH CHECK (public.is_service_role() OR id = public.current_end_user_id())',
            rec.schema_name
        );

        -- Rewrite storage_owner_access on storage_objects: permit service OR own row.
        EXECUTE format('DROP POLICY IF EXISTS storage_owner_access ON %I.storage_objects', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY storage_owner_access ON %I.storage_objects
             USING (public.is_service_role() OR uploaded_by = public.current_end_user_id())
             WITH CHECK (public.is_service_role() OR uploaded_by = public.current_end_user_id())',
            rec.schema_name
        );
    END LOOP;
END$$;

-- ============================================================================
-- provision_tenant: update so newly-created tenant schemas get the safe
-- forms from the start. Body is identical to 000037 except for:
--  - auth_uid() now delegates to public.current_end_user_id()
--  - user_self_access + storage_owner_access use is_service_role() OR own
-- ============================================================================

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
BEGIN
    v_schema_name := 'tenant_' || replace(p_project_id::text, '-', '_');

    EXECUTE format('CREATE SCHEMA %I', v_schema_name);
    EXECUTE format('SET search_path TO %I', v_schema_name);

    EXECUTE format(
        'CREATE TABLE %I.users (
            id                UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            email             TEXT        UNIQUE NOT NULL,
            password_hash     TEXT,
            display_name      TEXT,
            avatar_url        TEXT,
            metadata          JSONB       DEFAULT ''{}''::jsonb,
            provider          TEXT        DEFAULT ''email'',
            provider_user_id  TEXT,
            email_confirmed_at TIMESTAMPTZ,
            last_sign_in_at   TIMESTAMPTZ,
            banned_at         TIMESTAMPTZ,
            created_at        TIMESTAMPTZ DEFAULT now(),
            updated_at        TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );
    EXECUTE format('CREATE UNIQUE INDEX idx_users_provider ON %I.users(provider, provider_user_id) WHERE provider_user_id IS NOT NULL', v_schema_name);

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
            token_type  TEXT        NOT NULL CHECK (token_type IN (''verification'',''password_reset'')),
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
            key           TEXT        NOT NULL,
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

    EXECUTE format(
        'CREATE TABLE %I.vault_secrets (
            id          UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            name        TEXT        UNIQUE NOT NULL,
            secret      BYTEA       NOT NULL,
            nonce       BYTEA       NOT NULL,
            description TEXT        DEFAULT '''',
            created_at  TIMESTAMPTZ DEFAULT now(),
            updated_at  TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );

    EXECUTE format('ALTER TABLE %I.users ENABLE ROW LEVEL SECURITY', v_schema_name);
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
    EXECUTE format('CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY email_tokens_policy ON %I.email_tokens USING (true)', v_schema_name);
    EXECUTE format(
        'CREATE POLICY storage_owner_access ON %I.storage_objects
         USING (public.is_service_role() OR uploaded_by = public.current_end_user_id())
         WITH CHECK (public.is_service_role() OR uploaded_by = public.current_end_user_id())',
        v_schema_name
    );
    EXECUTE format('CREATE POLICY public_todos ON %I.todos FOR ALL USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY vault_secrets_policy ON %I.vault_secrets USING (true)', v_schema_name);

    -- Safe auth helpers — auth_uid() uses the public helper so it returns
    -- NULL on missing GUC instead of erroring.
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
           SELECT current_setting(''app.end_user_email'', true);
         $_$', v_schema_name
    );

    -- Grant the gateway runtime role access to this tenant schema.
    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', v_schema_name);

    UPDATE public.projects SET schema_name = v_schema_name WHERE id = p_project_id;
    SET search_path TO public;
END;
$fn$;

ALTER FUNCTION public.provision_tenant(UUID, TEXT, TEXT) OWNER TO eurobase_migrator;

COMMIT;
