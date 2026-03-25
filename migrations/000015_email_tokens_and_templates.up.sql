-- 000015_email_tokens_and_templates.up.sql
-- Email tokens (platform + tenant) and email templates.

BEGIN;

-----------------------------------------------------------------
-- Platform email tokens (console forgot-password)
-----------------------------------------------------------------
CREATE TABLE public.platform_email_tokens (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID NOT NULL REFERENCES platform_users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL,
    token_type  TEXT NOT NULL CHECK (token_type IN ('password_reset')),
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_platform_email_tokens_hash ON public.platform_email_tokens(token_hash);

-----------------------------------------------------------------
-- Per-project email templates
-----------------------------------------------------------------
CREATE TABLE public.email_templates (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id     UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    template_type  TEXT NOT NULL CHECK (template_type IN ('verification','password_reset','welcome','password_changed')),
    subject        TEXT NOT NULL,
    body_html      TEXT NOT NULL,
    created_at     TIMESTAMPTZ DEFAULT now(),
    updated_at     TIMESTAMPTZ DEFAULT now(),
    UNIQUE(project_id, template_type)
);

-----------------------------------------------------------------
-- Update provision_tenant() to include email_tokens table
-----------------------------------------------------------------
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

    -----------------------------------------------------------------
    -- users table (with auth columns)
    -----------------------------------------------------------------
    EXECUTE format(
        'CREATE TABLE %I.users (
            id               UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            email            TEXT        UNIQUE NOT NULL,
            password_hash    TEXT,
            display_name     TEXT,
            avatar_url       TEXT,
            metadata         JSONB       DEFAULT ''{}''::jsonb,
            email_confirmed_at TIMESTAMPTZ,
            last_sign_in_at  TIMESTAMPTZ,
            created_at       TIMESTAMPTZ DEFAULT now(),
            updated_at       TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );

    -----------------------------------------------------------------
    -- refresh_tokens table
    -----------------------------------------------------------------
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

    EXECUTE format(
        'CREATE INDEX idx_refresh_tokens_token_hash ON %I.refresh_tokens(token_hash)',
        v_schema_name
    );

    EXECUTE format(
        'CREATE INDEX idx_refresh_tokens_user_id ON %I.refresh_tokens(user_id)',
        v_schema_name
    );

    -----------------------------------------------------------------
    -- email_tokens table
    -----------------------------------------------------------------
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

    EXECUTE format(
        'CREATE INDEX idx_email_tokens_hash ON %I.email_tokens(token_hash)',
        v_schema_name
    );

    -----------------------------------------------------------------
    -- storage_objects table
    -----------------------------------------------------------------
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

    -----------------------------------------------------------------
    -- todos table (sample data for quickstart)
    -----------------------------------------------------------------
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

    -----------------------------------------------------------------
    -- Enable RLS on all tables
    -----------------------------------------------------------------
    EXECUTE format('ALTER TABLE %I.users ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.refresh_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.email_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos ENABLE ROW LEVEL SECURITY', v_schema_name);

    -----------------------------------------------------------------
    -- RLS policies
    -----------------------------------------------------------------
    EXECUTE format(
        'CREATE POLICY user_self_access ON %I.users
            USING (id = current_setting(''app.end_user_id'', true)::uuid)',
        v_schema_name
    );

    EXECUTE format(
        'CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens
            USING (true)',
        v_schema_name
    );

    EXECUTE format(
        'CREATE POLICY email_tokens_policy ON %I.email_tokens
            USING (true)',
        v_schema_name
    );

    EXECUTE format(
        'CREATE POLICY storage_owner_access ON %I.storage_objects
            USING (uploaded_by = current_setting(''app.end_user_id'', true)::uuid)',
        v_schema_name
    );

    EXECUTE format(
        'CREATE POLICY public_todos ON %I.todos
            FOR ALL USING (true)',
        v_schema_name
    );

    -----------------------------------------------------------------
    -- Update the projects table with the provisioned schema name
    -----------------------------------------------------------------
    UPDATE public.projects
       SET schema_name = v_schema_name
     WHERE id = p_project_id;

    SET search_path TO public;
END;
$$;

-----------------------------------------------------------------
-- Backfill: add email_tokens table to existing tenant schemas
-----------------------------------------------------------------
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT id, schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        -- Only create if table doesn't already exist
        IF NOT EXISTS (
            SELECT 1 FROM information_schema.tables
            WHERE table_schema = rec.schema_name AND table_name = 'email_tokens'
        ) THEN
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
                rec.schema_name, rec.schema_name
            );

            EXECUTE format(
                'CREATE INDEX ON %I.email_tokens(token_hash)',
                rec.schema_name
            );

            EXECUTE format(
                'ALTER TABLE %I.email_tokens ENABLE ROW LEVEL SECURITY',
                rec.schema_name
            );

            EXECUTE format(
                'CREATE POLICY email_tokens_policy ON %I.email_tokens
                    USING (true)',
                rec.schema_name
            );
        END IF;
    END LOOP;
END;
$$;

COMMIT;
