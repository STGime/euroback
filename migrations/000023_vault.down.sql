-- 000023_vault.down.sql
-- Drop vault_secrets table from all tenant schemas.

BEGIN;

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        EXECUTE format('DROP TABLE IF EXISTS %I.vault_secrets CASCADE', rec.schema_name);
    END LOOP;
END;
$$;

-- Revert provision_tenant() to version from migration 000022 (with auth helpers, without vault).
CREATE OR REPLACE FUNCTION public.provision_tenant(
    p_project_id   UUID,
    p_display_name TEXT,
    p_plan         TEXT DEFAULT 'free'
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
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

    EXECUTE format('ALTER TABLE %I.users ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.refresh_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.email_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos ENABLE ROW LEVEL SECURITY', v_schema_name);

    EXECUTE format('CREATE POLICY user_self_access ON %I.users USING (id = current_setting(''app.end_user_id'', true)::uuid)', v_schema_name);
    EXECUTE format('CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY email_tokens_policy ON %I.email_tokens USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY storage_owner_access ON %I.storage_objects USING (uploaded_by = current_setting(''app.end_user_id'', true)::uuid)', v_schema_name);
    EXECUTE format('CREATE POLICY public_todos ON %I.todos FOR ALL USING (true)', v_schema_name);

    -- GoTrue-compatible auth helper functions.
    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.auth_uid() RETURNS uuid
         LANGUAGE sql STABLE AS $fn$
           SELECT current_setting(''app.end_user_id'', true)::uuid;
         $fn$', v_schema_name
    );

    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.auth_role() RETURNS text
         LANGUAGE sql STABLE AS $fn$
           SELECT COALESCE(current_setting(''app.end_user_role'', true), ''anon'');
         $fn$', v_schema_name
    );

    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.auth_email() RETURNS text
         LANGUAGE sql STABLE AS $fn$
           SELECT current_setting(''app.end_user_email'', true);
         $fn$', v_schema_name
    );

    UPDATE public.projects SET schema_name = v_schema_name WHERE id = p_project_id;
    SET search_path TO public;
END;
$$;

COMMIT;
