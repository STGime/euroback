-- 000040_restore_phone_user_identities.up.sql
--
-- Regression repair. Migration 000037 (role split) rewrote
-- provision_tenant() with a body copy-pasted from 000023, silently
-- dropping additions that had landed in 000032 (user phone columns,
-- the user_identities table, the phone_verification token type,
-- email nullability). 000038 preserved the same oversight.
--
-- Result: any tenant provisioned on or after 2026-04-18 is missing
-- users.phone, users.phone_confirmed_at, the user_identities table,
-- and has a non-null email constraint. The console's Users tab
-- 500s on GET /users because the SELECT references u.phone.
--
-- Fix: back-fill the missing schema on every existing tenant, then
-- rewrite provision_tenant() with the full correct body so future
-- tenants are provisioned correctly.

BEGIN;

-- =============================================================
-- 1. Back-fill existing tenant schemas. Idempotent.
-- =============================================================

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        -- users.phone + users.phone_confirmed_at
        EXECUTE format('ALTER TABLE %I.users ADD COLUMN IF NOT EXISTS phone TEXT', rec.schema_name);
        EXECUTE format('ALTER TABLE %I.users ADD COLUMN IF NOT EXISTS phone_confirmed_at TIMESTAMPTZ', rec.schema_name);
        EXECUTE format('CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone ON %I.users(phone) WHERE phone IS NOT NULL', rec.schema_name);

        -- Make email nullable so phone-only users work. Idempotent: Postgres
        -- returns no-op if the column is already nullable.
        EXECUTE format('ALTER TABLE %I.users ALTER COLUMN email DROP NOT NULL', rec.schema_name);

        -- user_identities table.
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
        EXECUTE format('ALTER TABLE %I.user_identities ENABLE ROW LEVEL SECURITY', rec.schema_name);

        -- Permissive RLS on user_identities (gated upstream by service
        -- role / end-user-id checks in code). Drop-if-exists + create so
        -- re-running 000040 after a partial application still converges.
        EXECUTE format('DROP POLICY IF EXISTS user_identities_policy ON %I.user_identities', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY user_identities_policy ON %I.user_identities USING (true)',
            rec.schema_name
        );

        -- Grant the gateway role access to the new table (matches the
        -- grant structure applied by 000037 for other tenant tables).
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I.user_identities TO eurobase_gateway', rec.schema_name);

        -- Update email_tokens CHECK constraint to include phone_verification.
        -- Constraint name is auto-generated per tenant (CHECK without a name),
        -- so find it dynamically. Idempotent: if the token_type check already
        -- includes phone_verification, the rebuild is a no-op.
        DECLARE
            constraint_name TEXT;
            current_def     TEXT;
        BEGIN
            SELECT con.conname,
                   pg_get_constraintdef(con.oid)
              INTO constraint_name, current_def
              FROM pg_constraint con
              JOIN pg_class     cls ON cls.oid = con.conrelid
              JOIN pg_namespace nsp ON nsp.oid = cls.relnamespace
             WHERE nsp.nspname = rec.schema_name
               AND cls.relname = 'email_tokens'
               AND con.contype = 'c'
             LIMIT 1;

            IF constraint_name IS NOT NULL AND current_def IS NOT NULL
               AND position('phone_verification' IN current_def) = 0 THEN
                EXECUTE format('ALTER TABLE %I.email_tokens DROP CONSTRAINT %I', rec.schema_name, constraint_name);
                EXECUTE format(
                    'ALTER TABLE %I.email_tokens ADD CONSTRAINT %I CHECK (token_type IN (''verification'',''password_reset'',''magic_link'',''phone_verification''))',
                    rec.schema_name, constraint_name
                );
            END IF;
        END;
    END LOOP;
END$$;

-- =============================================================
-- 2. Rewrite provision_tenant() with the correct body.
-- =============================================================
-- Merges 000032 (phone + user_identities + nullable email +
-- phone_verification) with 000037's vault_secrets + gateway GRANTs
-- and 000038's safe auth_uid() / is_service_role() policies.

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
    EXECUTE format('CREATE POLICY user_identities_policy ON %I.user_identities USING (true)', v_schema_name);
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

    -- NULL-safe auth helpers. auth_uid() delegates to public.current_end_user_id()
    -- (which returns NULL when the GUC is empty rather than raising an
    -- invalid-uuid-cast error).
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
