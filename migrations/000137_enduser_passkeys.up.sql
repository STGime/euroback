-- #630: end-user passkeys (WebAuthn passwordless sign-in for a project's own
-- end users). Two new per-tenant SYSTEM tables:
--   * user_passkey_credentials — one row per enrolled credential (public key,
--     credential id / rawId, sign counter for clone detection, transports).
--   * webauthn_challenges — single-use ceremony state (register / login).
--
-- Security model (see the Notion plan): unlike the console's passkeys
-- (public.*, developer-pool-only), the end-user flow runs on the SDK path, so
-- these live in the tenant schema on the gateway-reachable surface and are
-- locked down instead: RLS INSERT/UPDATE is SERVICE-ROLE only (the auth
-- service sets the user_id server-side — an attacker can't plant a credential
-- against a victim = account takeover), a user can SELECT/DELETE only their
-- own, and the columns + tables are added to the platform-table lists so the
-- SDK data-API / SQL editor can't read or write them.
--
-- provision_tenant (copy of 000130) adds them for new tenants; the DO block
-- backfills existing tenants. No new SECURITY DEFINER public helper, so no
-- REVOKE. No explicit BEGIN/COMMIT: golang-migrate wraps the file.

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
            attempts    INT         NOT NULL DEFAULT 0,
            created_at  TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name, v_schema_name
    );
    EXECUTE format('CREATE INDEX idx_email_tokens_hash ON %I.email_tokens(token_hash)', v_schema_name);
    EXECUTE format('CREATE INDEX idx_email_tokens_user_type_expires ON %I.email_tokens(user_id, token_type, expires_at DESC)', v_schema_name);

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
        'CREATE TABLE %I.storage_shared_prefixes (
            prefix      TEXT        PRIMARY KEY
                        CHECK (prefix <> '''' AND right(prefix, 1) = ''/'' AND left(prefix, 1) <> ''/''
                               AND left(prefix, 8) <> ''exports/''),
            visibility  TEXT        NOT NULL CHECK (visibility IN (''authenticated'', ''public'')),
            created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
        )',
        v_schema_name
    );

    -- #630: end-user passkey credentials (one row per enrolled authenticator).
    -- credential_id is the WebAuthn rawId (the discoverable-login lookup key),
    -- unique within the tenant. public_key is the COSE key (not secret).
    EXECUTE format(
        'CREATE TABLE %I.user_passkey_credentials (
            id                UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            user_id           UUID        NOT NULL REFERENCES %I.users(id) ON DELETE CASCADE,
            credential_id     BYTEA       NOT NULL UNIQUE,
            public_key        BYTEA       NOT NULL,
            attestation_type  TEXT,
            transports        TEXT[],
            aaguid            BYTEA,
            sign_count        BIGINT      NOT NULL DEFAULT 0,
            nickname          TEXT,
            created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
            last_used_at      TIMESTAMPTZ
        )',
        v_schema_name, v_schema_name
    );
    EXECUTE format('CREATE INDEX idx_user_passkey_credentials_user_id ON %I.user_passkey_credentials(user_id)', v_schema_name);

    -- Single-use ceremony state. user_id nullable: a discoverable-login
    -- challenge is created before the user is known.
    EXECUTE format(
        'CREATE TABLE %I.webauthn_challenges (
            id           UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            user_id      UUID        REFERENCES %I.users(id) ON DELETE CASCADE,
            purpose      TEXT        NOT NULL CHECK (purpose IN (''register'', ''login'')),
            session_data JSONB       NOT NULL,
            expires_at   TIMESTAMPTZ NOT NULL,
            created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
        )',
        v_schema_name, v_schema_name
    );
    EXECUTE format('CREATE INDEX idx_webauthn_challenges_expires ON %I.webauthn_challenges(expires_at)', v_schema_name);

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
    EXECUTE format('ALTER TABLE %I.storage_shared_prefixes ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.user_passkey_credentials ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.webauthn_challenges ENABLE ROW LEVEL SECURITY', v_schema_name);
    -- Byte-order key index for SDK listings (keyset pagination, #697).
    EXECUTE format('CREATE INDEX idx_storage_objects_key_c ON %I.storage_objects (key COLLATE "C")', v_schema_name);
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
    -- #697: reads also cover developer files (no owner) in shared folders
    -- (storage_shared_prefixes); writes stay owner / service only, and end
    -- users can't create or move files into a shared folder. Export
    -- archives are never shared.
    EXECUTE format(
        'CREATE POLICY storage_read ON %I.storage_objects FOR SELECT
         USING (public.is_service_role() OR uploaded_by = public.current_end_user_id()
                OR (uploaded_by IS NULL AND left(key, 8) <> ''exports/'' AND EXISTS (
                      SELECT 1 FROM %I.storage_shared_prefixes s
                       WHERE starts_with(key, s.prefix)
                         AND (s.visibility = ''public'' OR public.current_end_user_id() IS NOT NULL))))',
        v_schema_name, v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY storage_insert ON %I.storage_objects FOR INSERT
         WITH CHECK (public.is_service_role()
                     OR (uploaded_by = public.current_end_user_id() AND NOT EXISTS (
                           SELECT 1 FROM %I.storage_shared_prefixes s WHERE starts_with(key, s.prefix))))',
        v_schema_name, v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY storage_update ON %I.storage_objects FOR UPDATE
         USING (public.is_service_role() OR uploaded_by = public.current_end_user_id())
         WITH CHECK (public.is_service_role()
                     OR (uploaded_by = public.current_end_user_id() AND NOT EXISTS (
                           SELECT 1 FROM %I.storage_shared_prefixes s WHERE starts_with(key, s.prefix))))',
        v_schema_name, v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY storage_delete ON %I.storage_objects FOR DELETE
         USING (public.is_service_role() OR uploaded_by = public.current_end_user_id())',
        v_schema_name
    );
    EXECUTE format('CREATE POLICY shared_prefixes_read ON %I.storage_shared_prefixes FOR SELECT USING (true)', v_schema_name);
    EXECUTE format(
        'CREATE POLICY shared_prefixes_write ON %I.storage_shared_prefixes FOR ALL
         USING (public.is_service_role()) WITH CHECK (public.is_service_role())',
        v_schema_name
    );
    -- #630: a user can SELECT/DELETE only their own passkeys (manage them via
    -- an authenticated session); INSERT/UPDATE is SERVICE-ROLE only (the auth
    -- service sets user_id server-side and bumps sign_count), so no end user
    -- or SQLi can plant a credential against another user.
    EXECUTE format(
        'CREATE POLICY passkey_select ON %I.user_passkey_credentials FOR SELECT
         USING (public.is_service_role() OR user_id = public.current_end_user_id())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY passkey_insert ON %I.user_passkey_credentials FOR INSERT
         WITH CHECK (public.is_service_role())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY passkey_update ON %I.user_passkey_credentials FOR UPDATE
         USING (public.is_service_role()) WITH CHECK (public.is_service_role())',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY passkey_delete ON %I.user_passkey_credentials FOR DELETE
         USING (public.is_service_role() OR user_id = public.current_end_user_id())',
        v_schema_name
    );
    -- Ceremony state is service-role only (begin/finish run server-side).
    EXECUTE format(
        'CREATE POLICY webauthn_challenges_policy ON %I.webauthn_challenges FOR ALL
         USING (public.is_service_role()) WITH CHECK (public.is_service_role())',
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
    EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', v_func_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.is_service_role() TO %I', v_func_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.current_end_user_id() TO %I', v_func_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.is_internal_auth_path() TO %I', v_func_role);

    v_ddl_role := v_schema_name || '_ddl';
    PERFORM public.provision_tenant_ddl_role(v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos OWNER TO %I', v_schema_name, v_ddl_role);

    -- The passkey tables are SYSTEM tables and must stay migrator-owned, but
    -- provision_tenant_ddl_role's system-table list predates them and so
    -- reassigns them to _ddl (the same drift 000136 fixed for
    -- storage_shared_prefixes). Re-assert them to the migrator — a tenant's
    -- own _ddl migrations must not own or alter them. (migrator can reassign
    -- from _ddl via its SET-membership of _ddl.)
    EXECUTE format('ALTER TABLE %I.user_passkey_credentials OWNER TO eurobase_migrator', v_schema_name);
    EXECUTE format('ALTER TABLE %I.webauthn_challenges OWNER TO eurobase_migrator', v_schema_name);

    UPDATE public.projects SET schema_name = v_schema_name WHERE id = p_project_id;
    SET search_path TO public;
END;
$fn$;

ALTER FUNCTION public.provision_tenant(UUID, TEXT, TEXT) OWNER TO eurobase_migrator;

-- Backfill existing tenant schemas on the shared cluster. (Team projects'
-- dedicated databases get the same from dedicated_bootstrap.sql.)
DO $$
DECLARE
    rec record;
BEGIN
    FOR rec IN
        SELECT p.schema_name
          FROM public.projects p
          JOIN pg_namespace n ON n.nspname = p.schema_name
         WHERE p.schema_name IS NOT NULL
    LOOP
        IF to_regclass(format('%I.user_passkey_credentials', rec.schema_name)) IS NOT NULL THEN
            RAISE EXCEPTION '000137: % already has a table named user_passkey_credentials', rec.schema_name;
        END IF;
        IF to_regclass(format('%I.webauthn_challenges', rec.schema_name)) IS NOT NULL THEN
            RAISE EXCEPTION '000137: % already has a table named webauthn_challenges', rec.schema_name;
        END IF;

        EXECUTE format(
            'CREATE TABLE %I.user_passkey_credentials (
                id                UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
                user_id           UUID        NOT NULL REFERENCES %I.users(id) ON DELETE CASCADE,
                credential_id     BYTEA       NOT NULL UNIQUE,
                public_key        BYTEA       NOT NULL,
                attestation_type  TEXT,
                transports        TEXT[],
                aaguid            BYTEA,
                sign_count        BIGINT      NOT NULL DEFAULT 0,
                nickname          TEXT,
                created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
                last_used_at      TIMESTAMPTZ
            )',
            rec.schema_name, rec.schema_name
        );
        EXECUTE format('CREATE INDEX idx_user_passkey_credentials_user_id ON %I.user_passkey_credentials(user_id)', rec.schema_name);
        EXECUTE format(
            'CREATE TABLE %I.webauthn_challenges (
                id           UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
                user_id      UUID        REFERENCES %I.users(id) ON DELETE CASCADE,
                purpose      TEXT        NOT NULL CHECK (purpose IN (''register'', ''login'')),
                session_data JSONB       NOT NULL,
                expires_at   TIMESTAMPTZ NOT NULL,
                created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
            )',
            rec.schema_name, rec.schema_name
        );
        EXECUTE format('CREATE INDEX idx_webauthn_challenges_expires ON %I.webauthn_challenges(expires_at)', rec.schema_name);

        EXECUTE format('ALTER TABLE %I.user_passkey_credentials ENABLE ROW LEVEL SECURITY', rec.schema_name);
        EXECUTE format('ALTER TABLE %I.webauthn_challenges ENABLE ROW LEVEL SECURITY', rec.schema_name);

        EXECUTE format(
            'CREATE POLICY passkey_select ON %I.user_passkey_credentials FOR SELECT
             USING (public.is_service_role() OR user_id = public.current_end_user_id())',
            rec.schema_name
        );
        EXECUTE format(
            'CREATE POLICY passkey_insert ON %I.user_passkey_credentials FOR INSERT
             WITH CHECK (public.is_service_role())',
            rec.schema_name
        );
        EXECUTE format(
            'CREATE POLICY passkey_update ON %I.user_passkey_credentials FOR UPDATE
             USING (public.is_service_role()) WITH CHECK (public.is_service_role())',
            rec.schema_name
        );
        EXECUTE format(
            'CREATE POLICY passkey_delete ON %I.user_passkey_credentials FOR DELETE
             USING (public.is_service_role() OR user_id = public.current_end_user_id())',
            rec.schema_name
        );
        EXECUTE format(
            'CREATE POLICY webauthn_challenges_policy ON %I.webauthn_challenges FOR ALL
             USING (public.is_service_role()) WITH CHECK (public.is_service_role())',
            rec.schema_name
        );

        -- Match provision_tenant's "ON ALL TABLES" grants for the new tables.
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I.user_passkey_credentials, %I.webauthn_challenges TO eurobase_gateway',
                       rec.schema_name, rec.schema_name);
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = rec.schema_name || '_func') THEN
            EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I.user_passkey_credentials, %I.webauthn_challenges TO %I',
                           rec.schema_name, rec.schema_name, rec.schema_name || '_func');
        END IF;
    END LOOP;
END $$;
