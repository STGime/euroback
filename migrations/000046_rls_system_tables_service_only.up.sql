-- 000046_rls_system_tables_service_only.up.sql
--
-- Closes advisory GHSA-wcg9-846j-ch78 (H8): permissive `USING (true)`
-- policies on per-tenant system tables let any code path that touches
-- them under the gateway role leak data across tenants.
--
-- Tightens four policies in every existing tenant schema and bakes the
-- new shape into provision_tenant() so future tenants get them by default:
--
--   refresh_tokens  → USING (public.is_service_role())     [auth-only]
--   email_tokens    → USING (public.is_service_role())     [auth-only]
--   vault_secrets   → USING (public.is_service_role())     [auth-only]
--   user_identities → USING (public.is_service_role() OR user_id = public.current_end_user_id())
--                                                          [own identities visible to the user]
--
-- The sample `todos` table keeps its permissive `USING (true)` policy
-- — it's the SDK demo surface and intentionally world-readable so the
-- "Hello, world" SDK example works. That isn't a regression: the demo
-- design has always been public, callers can apply a real preset via
-- the DDL endpoint.
--
-- Idempotent: each policy is DROP'd IF EXISTS before being recreated, so
-- re-running the migration is safe.
--
-- Authorisation: requires the executing role to be the owner of the
-- tenant tables. Tables provisioned via the post-#43/#44 path are owned
-- by `eurobase_migrator`. Older tables created via the pre-fix SDK DDL
-- path were owned by `eurobase_gateway`; migration 000045 grants
-- `eurobase_gateway TO eurobase_migrator`, so the migrate Job (running
-- as `eurobase_migrator`) can `SET LOCAL ROLE eurobase_gateway` to drop
-- gateway-owned policies. We do that defensively per-table.

BEGIN;

DO $$
DECLARE
    v_schema TEXT;
    v_owner  TEXT;
    v_table  TEXT;
BEGIN
    FOR v_schema IN
        SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        FOREACH v_table IN ARRAY ARRAY['refresh_tokens', 'email_tokens', 'vault_secrets', 'user_identities']
        LOOP
            -- Find the table's owner so we can run DROP/CREATE POLICY as that role.
            -- Skip if the table doesn't exist (older tenants might predate vault_secrets).
            EXECUTE format(
                'SELECT tableowner FROM pg_tables WHERE schemaname = %L AND tablename = %L',
                v_schema, v_table
            ) INTO v_owner;
            IF v_owner IS NULL THEN
                CONTINUE;
            END IF;

            EXECUTE format('SET LOCAL ROLE %I', v_owner);

            CASE v_table
                WHEN 'refresh_tokens' THEN
                    EXECUTE format('DROP POLICY IF EXISTS refresh_tokens_policy ON %I.refresh_tokens', v_schema);
                    EXECUTE format(
                        'CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens
                            USING (public.is_service_role())
                            WITH CHECK (public.is_service_role())',
                        v_schema
                    );
                WHEN 'email_tokens' THEN
                    EXECUTE format('DROP POLICY IF EXISTS email_tokens_policy ON %I.email_tokens', v_schema);
                    EXECUTE format(
                        'CREATE POLICY email_tokens_policy ON %I.email_tokens
                            USING (public.is_service_role())
                            WITH CHECK (public.is_service_role())',
                        v_schema
                    );
                WHEN 'vault_secrets' THEN
                    EXECUTE format('DROP POLICY IF EXISTS vault_secrets_policy ON %I.vault_secrets', v_schema);
                    EXECUTE format(
                        'CREATE POLICY vault_secrets_policy ON %I.vault_secrets
                            USING (public.is_service_role())
                            WITH CHECK (public.is_service_role())',
                        v_schema
                    );
                WHEN 'user_identities' THEN
                    EXECUTE format('DROP POLICY IF EXISTS user_identities_policy ON %I.user_identities', v_schema);
                    EXECUTE format(
                        'CREATE POLICY user_identities_policy ON %I.user_identities
                            USING (public.is_service_role() OR user_id = public.current_end_user_id())
                            WITH CHECK (public.is_service_role() OR user_id = public.current_end_user_id())',
                        v_schema
                    );
            END CASE;

            RESET ROLE;
        END LOOP;

        RAISE NOTICE 'tightened RLS policies on tenant schema %', v_schema;
    END LOOP;
END$$;

-- =============================================================
-- Update provision_tenant() so new tenants get the tightened policies.
-- The body is identical to 000040 except for the four policy lines.
-- =============================================================
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
    -- GHSA-wcg9-846j-ch78: tightened from USING(true).
    EXECUTE format(
        'CREATE POLICY user_identities_policy ON %I.user_identities
         USING (public.is_service_role() OR user_id = public.current_end_user_id())
         WITH CHECK (public.is_service_role() OR user_id = public.current_end_user_id())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens
         USING (public.is_service_role())
         WITH CHECK (public.is_service_role())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY email_tokens_policy ON %I.email_tokens
         USING (public.is_service_role())
         WITH CHECK (public.is_service_role())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY storage_owner_access ON %I.storage_objects
         USING (public.is_service_role() OR uploaded_by = public.current_end_user_id())
         WITH CHECK (public.is_service_role() OR uploaded_by = public.current_end_user_id())',
        v_schema_name
    );
    -- todos is the SDK sample table; intentionally world-readable so the
    -- demo Hello-World query works without needing auth setup.
    EXECUTE format('CREATE POLICY public_todos ON %I.todos FOR ALL USING (true)', v_schema_name);
    EXECUTE format(
        'CREATE POLICY vault_secrets_policy ON %I.vault_secrets
         USING (public.is_service_role())
         WITH CHECK (public.is_service_role())',
        v_schema_name
    );

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
