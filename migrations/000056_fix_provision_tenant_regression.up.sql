-- 000056_fix_provision_tenant_regression.up.sql
--
-- HOTFIX (P0). Repairs a regression introduced by
-- 000055_mcp_intent_gate.up.sql.
--
-- What happened
-- =============
-- 000055's intended change was correct and stays: it tightened the RLS
-- policies on refresh_tokens / email_tokens / vault_secrets to
-- `public.is_internal_auth_path()`. But to apply it, the migration
-- re-pasted the entire provision_tenant() body and used a STALE copy.
-- That stale copy silently reverted two table definitions:
--
--   * vault_secrets:   column `secret`     -> `ciphertext`
--   * storage_objects: `size_bytes` + content_type + metadata +
--                      uploaded_by FK (000050/000052 shape) -> a bare
--                      `size BIGINT` / `uploaded_by` with no FK.
--
-- Because provision_tenant() is CREATE OR REPLACE, the broken body is
-- what the live database now holds. EXISTING tenants are unaffected
-- (they were provisioned by earlier, correct revisions, and the live
-- DB confirms every tenant_*.vault_secrets still has `secret`/`nonce`).
-- But ANY NEW project created after 000055 gets a tenant whose vault
-- and storage are broken:
--   * internal/vault/service.go queries `secret`/`nonce` -> fails
--     ("column \"secret\" does not exist").
--   * storage writes reference size_bytes/content_type/metadata -> fail.
--
-- The fix
-- =======
-- Restore provision_tenant() to the correct 000052 schema body, keeping
-- 000055's security hardening: the three sensitive tables use
-- `public.is_internal_auth_path()`, everything else uses
-- `public.is_service_role()` exactly as 000055 intended.
--
-- This migration ONLY redefines the function (affects future tenants).
-- No existing-tenant backfill is needed for the schema regression
-- because no existing tenant ever received the broken shape, and
-- 000055 already migrated existing-tenant POLICIES to
-- is_internal_auth_path().
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its
-- own transaction.

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

    -- RESTORED to the correct 000050/000052 shape (000055 had reverted
    -- this to a bare `size`/no-FK form).
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

    -- RESTORED to the correct `secret`/`nonce` columns that
    -- internal/vault/service.go and every existing tenant use
    -- (000055 had reverted this to `ciphertext`).
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
    EXECUTE format(
        'CREATE POLICY user_identities_policy ON %I.user_identities
         USING (public.is_service_role() OR user_id = public.current_end_user_id())
         WITH CHECK (public.is_service_role() OR user_id = public.current_end_user_id())',
        v_schema_name
    );
    -- KEPT from 000055 (#164): the three sensitive tables check the
    -- stricter intent GUC, not is_service_role(). Only edb.RunAsAuthService
    -- sets app.intent='internal_auth_path'; the generic SQL handler does
    -- not, which blocks the MCP prompt-injection exfiltration path.
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
    -- Live lookup against users(id), per 000052 (#55).
    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.auth_email() RETURNS text
         LANGUAGE sql STABLE AS $_$
           SELECT email FROM %I.users WHERE id = public.current_end_user_id();
         $_$', v_schema_name, v_schema_name
    );

    -- Grant the gateway runtime role access to this tenant schema.
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
    EXECUTE format('GRANT %I TO eurobase_function_runner', v_func_role);

    UPDATE public.projects SET schema_name = v_schema_name WHERE id = p_project_id;
    SET search_path TO public;
END;
$fn$;

ALTER FUNCTION public.provision_tenant(UUID, TEXT, TEXT) OWNER TO eurobase_migrator;
