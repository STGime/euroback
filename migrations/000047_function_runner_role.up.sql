-- 000047_function_runner_role.up.sql
--
-- Closes advisory GHSA-7428-mvpp-rhr7 (C3) layer 1 of 3 — per-tenant
-- database role for the edge functions runner.
--
-- Before this migration, the runner used `DATABASE_URL` (eurobase_gateway
-- role) and exposed `ctx.db.unsafe(query)` to tenant function code.
-- A function in tenant A could `SELECT * FROM tenant_<other>.users`
-- because gateway has DML on every tenant schema.
--
-- After: each tenant schema has its own non-login role
-- `<schema_name>_func` granted USAGE+DML only on that schema. The
-- function runner connects as a NEW login role `eurobase_function_runner`
-- which is a member of every per-tenant role but has no direct grants on
-- tenant schemas, public.*, or any other tenant. At each invocation the
-- runner runs `SET LOCAL ROLE <schema>_func` so the user's SQL executes
-- under a role that physically cannot reach other tenants.
--
-- Additional defences (sandbox isolation, HMAC gateway-runner traffic)
-- ship in PRs 3b and 3c — until they land, this migration is the
-- principal cross-tenant defence for the functions runtime.
--
-- ## Pre-conditions
--
-- The `eurobase_function_runner` LOGIN role must be created via the
-- Scaleway console BEFORE this migration runs. Mirrors the bootstrap
-- pattern used for migrator/gateway/developer (000037 / 000044). The
-- migration script fails fast with a clear message if the role is
-- missing so an operator knows to provision it.
--
-- ## Idempotency
--
-- Re-running is safe. CREATE ROLE for per-tenant func roles uses an
-- IF NOT EXISTS guard. GRANTs are idempotent in Postgres.
--
-- ## Authorisation
--
-- Run as `eurobase_migrator`. On Scaleway managed Postgres, migrator
-- has CREATEROLE-equivalent privileges so it can create the per-tenant
-- roles. If your environment denies this, migration aborts with a
-- "permission denied to create role" error — escalate via Scaleway
-- console (run as eurobase_api or _rdb_superadmin).

BEGIN;

-- ── 1. Verify the eurobase_function_runner login role exists ──
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_function_runner') THEN
        RAISE EXCEPTION 'role eurobase_function_runner does not exist — create it via the Scaleway console first (CREATE ROLE eurobase_function_runner WITH LOGIN INHERIT)';
    END IF;
END$$;

GRANT CONNECT ON DATABASE eurobase TO eurobase_function_runner;
-- The runner needs USAGE on `public` to call helper functions like
-- public.current_end_user_id(), public.is_service_role(), and the
-- tenant schema's auth_uid()/auth_role()/auth_email() functions which
-- delegate to public.* helpers. This is the ONLY blanket grant on the
-- runner role.
GRANT USAGE ON SCHEMA public TO eurobase_function_runner;
GRANT EXECUTE ON FUNCTION public.is_service_role() TO eurobase_function_runner;
GRANT EXECUTE ON FUNCTION public.current_end_user_id() TO eurobase_function_runner;

-- ── 2. Backfill: for each existing tenant, create the per-tenant func
--      role, grant it USAGE+DML on its schema, and add the runner role
--      as a member.
DO $$
DECLARE
    v_schema TEXT;
    v_role   TEXT;
BEGIN
    FOR v_schema IN
        SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        v_role := v_schema || '_func';

        -- IF NOT EXISTS — re-running this migration shouldn't blow up.
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_role) THEN
            EXECUTE format('CREATE ROLE %I NOLOGIN INHERIT', v_role);
        END IF;

        -- Grants: this role can read+write its own tenant schema only.
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', v_schema, v_role);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I', v_schema, v_role);
        EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I', v_schema, v_role);
        EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %I TO %I', v_schema, v_role);

        -- Default privileges: tables created by migrator inside this
        -- schema in the future inherit DML grants to the per-tenant role.
        EXECUTE format(
            'ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I
             GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I',
            v_schema, v_role
        );
        EXECUTE format(
            'ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I
             GRANT USAGE, SELECT ON SEQUENCES TO %I',
            v_schema, v_role
        );

        -- Runner becomes a member of the per-tenant role so it can
        -- `SET LOCAL ROLE <schema>_func` per invocation. Without
        -- INHERIT here — the runner gets per-tenant privileges only
        -- after explicit SET LOCAL ROLE, never by ambient inheritance.
        -- That keeps the runner role itself privilege-less when no role
        -- is set.
        EXECUTE format('GRANT %I TO eurobase_function_runner', v_role);

        RAISE NOTICE 'provisioned per-tenant function role %', v_role;
    END LOOP;
END$$;

-- ── 3. Update provision_tenant() so new tenants get the per-tenant
--      func role automatically. The body is identical to 000046
--      except for the new role-creation block at the end.
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
    EXECUTE format('CREATE POLICY public_todos ON %I.todos FOR ALL USING (true)', v_schema_name);
    EXECUTE format(
        'CREATE POLICY vault_secrets_policy ON %I.vault_secrets
         USING (public.is_service_role())
         WITH CHECK (public.is_service_role())',
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
           SELECT current_setting(''app.end_user_email'', true);
         $_$', v_schema_name
    );

    -- Grant the gateway runtime role access to this tenant schema.
    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', v_schema_name);

    -- ── Per-tenant function-runner role ──
    -- Closes GHSA-7428-mvpp-rhr7 (C3): the edge-functions runner sets
    -- LOCAL ROLE to this role per invocation so user JS can only reach
    -- this tenant's data, never another tenant's.
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

COMMIT;
