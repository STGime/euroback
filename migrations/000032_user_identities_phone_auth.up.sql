-- 000032_user_identities_phone_auth.up.sql
-- Phase 1: Add user_identities table for multi-provider OAuth linking.
-- Phase 2: Add phone auth columns and phone_verification token type.

BEGIN;

-----------------------------------------------------------------
-- 1. Add phone columns and user_identities to existing tenants
-----------------------------------------------------------------
DO $$
DECLARE
    rec RECORD;
    con_name TEXT;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        -- Add phone columns to users table.
        EXECUTE format('ALTER TABLE %I.users ADD COLUMN IF NOT EXISTS phone TEXT', rec.schema_name);
        EXECUTE format('ALTER TABLE %I.users ADD COLUMN IF NOT EXISTS phone_confirmed_at TIMESTAMPTZ', rec.schema_name);
        EXECUTE format('CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone ON %I.users(phone) WHERE phone IS NOT NULL', rec.schema_name);
        -- Make email nullable for phone-only users.
        EXECUTE format('ALTER TABLE %I.users ALTER COLUMN email DROP NOT NULL', rec.schema_name);

        -- Create user_identities table.
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I.user_identities (
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
            rec.schema_name, rec.schema_name
        );
        EXECUTE format('CREATE INDEX IF NOT EXISTS idx_user_identities_user_id ON %I.user_identities(user_id)', rec.schema_name);

        -- Enable RLS on user_identities.
        EXECUTE format('ALTER TABLE %I.user_identities ENABLE ROW LEVEL SECURITY', rec.schema_name);
        -- Only internal access (auth service uses service role, not end-user JWT).
        BEGIN
            EXECUTE format('CREATE POLICY user_identities_policy ON %I.user_identities USING (true)', rec.schema_name);
        EXCEPTION WHEN duplicate_object THEN
            NULL; -- policy already exists
        END;

        -- Backfill: create identity rows from existing provider/provider_user_id columns.
        EXECUTE format(
            'INSERT INTO %I.user_identities (user_id, provider, provider_user_id, identity_data, last_sign_in_at, created_at)
             SELECT id, provider, provider_user_id, ''{}''::jsonb, last_sign_in_at, created_at
             FROM %I.users
             WHERE provider IS NOT NULL AND provider != ''email'' AND provider_user_id IS NOT NULL
             ON CONFLICT (provider, provider_user_id) DO NOTHING',
            rec.schema_name, rec.schema_name
        );

        -- Update email_tokens CHECK to include phone_verification.
        SELECT c.conname INTO con_name
        FROM pg_constraint c
        JOIN pg_class r ON c.conrelid = r.oid
        JOIN pg_namespace n ON r.relnamespace = n.oid
        WHERE n.nspname = rec.schema_name
          AND r.relname = 'email_tokens'
          AND c.contype = 'c'
          AND pg_get_constraintdef(c.oid) LIKE '%token_type%';

        IF con_name IS NOT NULL THEN
            EXECUTE format(
                'ALTER TABLE %I.email_tokens DROP CONSTRAINT %I',
                rec.schema_name, con_name
            );
            EXECUTE format(
                'ALTER TABLE %I.email_tokens ADD CONSTRAINT %I CHECK (token_type IN (''verification'',''password_reset'',''magic_link'',''phone_verification''))',
                rec.schema_name, con_name
            );
        END IF;
    END LOOP;
END;
$$;

-----------------------------------------------------------------
-- 2. Update provision_tenant() with user_identities + phone
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
    EXECUTE format('ALTER TABLE %I.user_identities ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.refresh_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.email_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos ENABLE ROW LEVEL SECURITY', v_schema_name);

    EXECUTE format('CREATE POLICY user_self_access ON %I.users USING (id = current_setting(''app.end_user_id'', true)::uuid)', v_schema_name);
    EXECUTE format('CREATE POLICY user_identities_policy ON %I.user_identities USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY email_tokens_policy ON %I.email_tokens USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY storage_owner_access ON %I.storage_objects USING (uploaded_by = current_setting(''app.end_user_id'', true)::uuid)', v_schema_name);
    EXECUTE format('CREATE POLICY public_todos ON %I.todos FOR ALL USING (true)', v_schema_name);

    UPDATE public.projects SET schema_name = v_schema_name WHERE id = p_project_id;
    SET search_path TO public;
END;
$$;

COMMIT;
