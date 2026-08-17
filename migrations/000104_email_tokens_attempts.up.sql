-- 000104_email_tokens_attempts.up.sql
--
-- Closes #233. The `email_tokens` table stores 6-digit phone OTPs
-- (token_type='phone_verification'), 32-hex email verification tokens,
-- password-reset tokens, and magic-link tokens. The phone-OTP path is
-- the only branch with a tenant-wide brute-force weakness worth calling
-- out at P1:
--
--   VerifyOTP() matched by `token_hash` alone across the entire tenant
--   schema with no per-token attempt cap. A 6-digit code has ~10^6
--   guesses; a modest bot could grind against the space and land a
--   valid code for SOME phone in the project.
--
-- Two changes in this migration make that attack unaffordable:
--
--   1. `attempts` counter per token. The verify path binds a guess to
--      the token that belongs to the phone being attempted (see the
--      code side in internal/sms/service.go) and increments this
--      counter on each wrong hash. After 5 wrong guesses the token is
--      marked used_at=now() and can never verify — the attacker gets
--      6 tries per issued code, not 10^6.
--
--   2. `(user_id, token_type, expires_at DESC)` composite index. The
--      new verify SQL joins by user (looked up from phone) and picks
--      the most-recent active token; the previous hash-only index
--      wouldn't be used by that plan.
--
-- The other three token_type values (verification / password_reset /
-- magic_link) share the column but keep their existing hash-only
-- verify path — the tokens are 32 hex chars (~10^38 space) so the
-- brute-force calculus doesn't apply. The `attempts` column defaults
-- to 0 for them and is simply ignored.

BEGIN;

-----------------------------------------------------------------
-- 1. Add attempts column to every existing tenant's email_tokens
-----------------------------------------------------------------
-- ADD COLUMN IF NOT EXISTS is idempotent — safe to re-run and safe
-- against a partial prior run.
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        EXECUTE format(
            'ALTER TABLE %I.email_tokens ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0',
            rec.schema_name
        );
        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS idx_email_tokens_user_type_expires ON %I.email_tokens(user_id, token_type, expires_at DESC)',
            rec.schema_name
        );
    END LOOP;
END;
$$;

-----------------------------------------------------------------
-- 2. Redefine provision_tenant so NEW tenants ship the column
-----------------------------------------------------------------
-- Full-body redefinition mirrors 000063 with two deltas:
--   * email_tokens.CREATE TABLE gains `attempts INT NOT NULL DEFAULT 0`
--   * additional (user_id, token_type, expires_at DESC) index for the
--     phone-scoped verify plan
-- Everything else is identical to 000063 to preserve behaviour.
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

    -- #233: `attempts` counts wrong-hash verify calls against this
    -- specific token. The phone-OTP verify path (see
    -- internal/sms/service.go) marks used_at=now() at attempts=5 to
    -- kill the token. Other token types keep the column defaulted to
    -- 0; they use a 32-hex space where per-token throttling is
    -- unnecessary.
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
    EXECUTE format('GRANT %I TO eurobase_function_runner', v_func_role);

    v_ddl_role := v_schema_name || '_ddl';
    PERFORM public.provision_tenant_ddl_role(v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos OWNER TO %I', v_schema_name, v_ddl_role);

    UPDATE public.projects SET schema_name = v_schema_name WHERE id = p_project_id;
    SET search_path TO public;
END;
$fn$;

ALTER FUNCTION public.provision_tenant(UUID, TEXT, TEXT) OWNER TO eurobase_migrator;

COMMIT;
