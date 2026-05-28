-- 000055_mcp_intent_gate.up.sql
--
-- Closes #164 (P0). Mitigates the Eurobase-side exposure to the Supabase
-- MCP prompt-injection vulnerability (General Analysis, 2026-04).
--
-- Problem
-- =======
-- `refresh_tokens`, `email_tokens`, `vault_secrets` policies today require
-- `public.is_service_role()` — which checks `app.end_user_role='service'`.
-- The platform `/data/sql` handler that backs the MCP server's `runSQL`
-- tool sets exactly that GUC for developer-authenticated traffic. So a
-- developer who uses Claude Code / Cursor's MCP integration to view rows
-- has those tables RLS-readable from any prompt-injected SQL call.
--
-- The mitigation: introduce a SECOND, more-specific GUC
-- `app.intent='internal_auth_path'` and a helper
-- `public.is_internal_auth_path()`. Only the legitimate auth / email /
-- vault Go code paths set it (via the new edb.RunAsAuthService helper).
-- The generic SQL handler does NOT set it, so a prompt-injected runSQL
-- can no longer reach these tables — RLS rejects at the policy layer.
--
-- `is_service_role()` still works for the other tables (`users`,
-- `user_identities`, `storage_objects`) where legitimate admin paths
-- (DSAR export, platform admin, etc.) need broad access. We narrow only
-- the credential / secret tables that have no business being read by
-- arbitrary developer SQL.
--
-- No explicit BEGIN/COMMIT — golang-migrate wraps each .up.sql file.

-- ── 1. New helper function ──────────────────────────────────────────
CREATE OR REPLACE FUNCTION public.is_internal_auth_path() RETURNS boolean
    LANGUAGE sql STABLE AS $$
    SELECT current_setting('app.intent', true) = 'internal_auth_path'
$$;

ALTER FUNCTION public.is_internal_auth_path() OWNER TO eurobase_migrator;
GRANT EXECUTE ON FUNCTION public.is_internal_auth_path() TO eurobase_gateway;
GRANT EXECUTE ON FUNCTION public.is_internal_auth_path() TO eurobase_developer;
GRANT EXECUTE ON FUNCTION public.is_internal_auth_path() TO eurobase_api;

-- ── 2. Backfill policies on every existing tenant schema ────────────
-- Drop the old `is_service_role()`-only policies and replace with
-- `is_internal_auth_path()`. Same shape but a strictly tighter guard.
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        -- refresh_tokens
        EXECUTE format('DROP POLICY IF EXISTS refresh_tokens_policy ON %I.refresh_tokens', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens
             USING (public.is_internal_auth_path())
             WITH CHECK (public.is_internal_auth_path())',
            rec.schema_name
        );

        -- email_tokens
        EXECUTE format('DROP POLICY IF EXISTS email_tokens_policy ON %I.email_tokens', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY email_tokens_policy ON %I.email_tokens
             USING (public.is_internal_auth_path())
             WITH CHECK (public.is_internal_auth_path())',
            rec.schema_name
        );

        -- vault_secrets
        EXECUTE format('DROP POLICY IF EXISTS vault_secrets_policy ON %I.vault_secrets', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY vault_secrets_policy ON %I.vault_secrets
             USING (public.is_internal_auth_path())
             WITH CHECK (public.is_internal_auth_path())',
            rec.schema_name
        );
    END LOOP;
END$$;

-- ── 3. provision_tenant: new tenants get the locked-down policies ───
-- Same body as 000052 except the three sensitive tables now check
-- is_internal_auth_path() instead of is_service_role(). Everything
-- else is unchanged.
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
    EXECUTE format('CREATE INDEX idx_email_tokens_token_hash ON %I.email_tokens(token_hash)', v_schema_name);

    EXECUTE format(
        'CREATE TABLE %I.storage_objects (
            id            UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            key           TEXT        NOT NULL,
            content_type  TEXT,
            size          BIGINT,
            uploaded_by   UUID,
            created_at    TIMESTAMPTZ DEFAULT now(),
            UNIQUE(key)
        )',
        v_schema_name
    );

    EXECUTE format(
        'CREATE TABLE %I.vault_secrets (
            id          UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            name        TEXT        NOT NULL UNIQUE,
            ciphertext  BYTEA       NOT NULL,
            nonce       BYTEA       NOT NULL,
            description TEXT,
            created_at  TIMESTAMPTZ DEFAULT now(),
            updated_at  TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );

    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I.todos (
            id         UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            user_id    UUID,
            title      TEXT        NOT NULL,
            completed  BOOLEAN     DEFAULT false,
            created_at TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );

    -- Enable RLS + policies on every table.
    EXECUTE format('ALTER TABLE %I.users ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.user_identities ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.refresh_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.email_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.vault_secrets ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos ENABLE ROW LEVEL SECURITY', v_schema_name);

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
    -- The three sensitive tables now check the stricter intent GUC.
    -- Only edb.RunAsAuthService sets it; the generic SQL handler does
    -- NOT, which blocks the MCP prompt-injection exfiltration path.
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

    -- Grants — preserved from prior provision_tenant rev.
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT CREATE ON SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT ALL ON ALL TABLES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT ALL ON ALL SEQUENCES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT ALL ON ALL FUNCTIONS IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT ALL ON TABLES TO eurobase_gateway', v_schema_name);
    EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT ALL ON SEQUENCES TO eurobase_gateway', v_schema_name);

    -- Per-tenant function-runner role (created by 000047).
    EXECUTE format('CREATE ROLE %I', v_func_role);
    EXECUTE format('GRANT %I TO eurobase_function_runner', v_func_role);
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', v_schema_name, v_func_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I', v_schema_name, v_func_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %I TO %I', v_schema_name, v_func_role);
END;
$fn$;

ALTER FUNCTION public.provision_tenant(UUID, TEXT, TEXT) OWNER TO eurobase_migrator;
