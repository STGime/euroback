-- dedicated_bootstrap.sql
--
-- Team-tier M2.5 part 2b — one-shot bootstrap for a fresh Scaleway
-- managed-PG instance so tenant SDK traffic can safely route to it.
--
-- Runs as the Scaleway-provisioned owner (`eurobase_owner`) via the
-- Go Bootstrapper (internal/dbprovider/bootstrap.go). Idempotent —
-- safe to re-run on retry (`CREATE ... IF NOT EXISTS`, `CREATE OR
-- REPLACE FUNCTION`, `DO $$ ... IF NOT EXISTS ... $$`).
--
-- Scope on the dedicated instance is deliberately smaller than the
-- shared cluster's provision_tenant (migration 000063):
--
--   * NO `eurobase_migrator` role — the Scaleway-provisioned owner
--     IS the effective migrator (owns tables, does DDL).
--   * NO `eurobase_function_runner` role — edge functions running
--     against a Team-tier dedicated instance is a follow-up
--     (routing the runner pod is a separate design).
--   * NO `<schema>_ddl` role — tenant migrations against a
--     dedicated instance land in part 2c (migration executor
--     routing).
--   * NO `<schema>_func` role — same reason as `_function_runner`
--     above.
--   * NO `public.projects` / `public.tenant_migrations` — those
--     are platform tables that stay on the shared cluster.
--
-- What DOES land here:
--   1. `uuid-ossp` extension
--   2. Three GUC-reading helper functions in `public`:
--        is_service_role(), current_end_user_id(),
--        is_internal_auth_path()
--   3. `eurobase_gateway` LOGIN role — the non-owner runtime user
--      SDK traffic connects as. Password is set via a second SQL
--      statement issued by the Go Bootstrapper (kept out of this
--      file so the password never lands in source or logs).
--   4. `provision_tenant(project_id UUID, display_name TEXT)` —
--      a slimmed clone of the shared cluster's function. Creates
--      the tenant schema, the six standard tables, RLS policies,
--      per-tenant auth_* helpers, and grants to `eurobase_gateway`.
--
-- Everything owned by `eurobase_owner`. RLS enforcement works
-- because SDK traffic never authenticates as the owner — see the
-- loud warning at TEAM_TIER_ROUTING in gateway/router.go.

-- ============================================================================
-- 1. Extensions
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ============================================================================
-- 2. GUC-reading helpers (mirrors shared-cluster migrations 000038 + 000055)
-- ============================================================================
--
-- These are the primitives every RLS policy on tenant tables consults:
--   * current_end_user_id() → who's making the request (JWT-derived
--     UUID on `app.end_user_id`, NULL for anon)
--   * is_service_role() → is this a secret-key SDK caller? (grants
--     the RLS-bypass branch)
--   * is_internal_auth_path() → is this the platform's own auth
--     endpoint touching refresh_tokens / email_tokens / vault?
--     (tighter than is_service_role — only set by internal helpers)

CREATE OR REPLACE FUNCTION public.current_end_user_id() RETURNS uuid
    LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.end_user_id', true), '')::uuid
$$;

CREATE OR REPLACE FUNCTION public.is_service_role() RETURNS boolean
    LANGUAGE sql STABLE AS $$
    SELECT current_setting('app.end_user_role', true) = 'service'
$$;

CREATE OR REPLACE FUNCTION public.is_internal_auth_path() RETURNS boolean
    LANGUAGE sql STABLE AS $$
    SELECT current_setting('app.intent', true) = 'internal_auth_path'
$$;

-- ============================================================================
-- 3. Runtime role
-- ============================================================================
--
-- Created NOLOGIN here so this file can be applied without a
-- password (the file lives in the repo). The Go Bootstrapper follows
-- this with `ALTER ROLE eurobase_gateway WITH LOGIN PASSWORD '<x>'`
-- outside the source-tree bundle, keeping secrets out of Git.
--
-- Non-owner by construction: `eurobase_owner` owns every table
-- provision_tenant creates below, so RLS policies are enforced when
-- eurobase_gateway connects (Postgres skips RLS only for the table
-- owner).

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_gateway') THEN
        CREATE ROLE eurobase_gateway NOLOGIN;
    END IF;
END $$;

GRANT EXECUTE ON FUNCTION public.current_end_user_id() TO eurobase_gateway;
GRANT EXECUTE ON FUNCTION public.is_service_role() TO eurobase_gateway;
GRANT EXECUTE ON FUNCTION public.is_internal_auth_path() TO eurobase_gateway;
GRANT EXECUTE ON FUNCTION public.uuid_generate_v4() TO eurobase_gateway;

-- ============================================================================
-- 4. provision_tenant
-- ============================================================================
--
-- Slimmed clone of the shared cluster's public.provision_tenant
-- (migrations 000060/000063). Same table shape, same RLS policy
-- shape, same per-tenant auth_* helpers — an SDK payload that runs
-- on the shared cluster today runs identically here.
--
-- Differences from the shared-cluster version, all deliberate:
--   * No `_ddl` role creation (`provision_tenant_ddl_role` call
--     dropped). Tenant migrations on dedicated arrive in part 2c.
--   * No `_func` role creation. Edge functions on dedicated arrive
--     as a separate follow-up.
--   * No `ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator …`.
--     Everything is owned by eurobase_owner; new tables created by
--     provision_tenant already inherit the correct grants via the
--     GRANT statements at the end of the function body.
--   * No `UPDATE public.projects SET schema_name = v_schema_name …`
--     — public.projects does not exist on this instance. The
--     platform's Repo layer already knows the schema_name of a
--     Team-tier project (it was set at create time on the shared
--     cluster's projects row).

CREATE OR REPLACE FUNCTION public.provision_tenant(
    p_project_id   UUID,
    p_display_name TEXT
)
RETURNS TEXT
LANGUAGE plpgsql
SECURITY DEFINER
AS $fn$
DECLARE
    v_schema_name TEXT;
BEGIN
    v_schema_name := 'tenant_' || replace(p_project_id::text, '-', '_');

    -- Idempotency guard: if the schema already exists, this is a
    -- retried bootstrap for the same project. Return the schema
    -- name and exit cleanly. The Go Bootstrapper treats "schema
    -- already there" as success.
    IF EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = v_schema_name) THEN
        RETURN v_schema_name;
    END IF;

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

    -- Enable RLS everywhere. Note we do NOT `FORCE ROW LEVEL
    -- SECURITY` — the owner (eurobase_owner) is intentionally
    -- allowed to bypass RLS for DDL and admin work via the M4
    -- Direct Connection UI. Only the non-owner runtime role
    -- (eurobase_gateway) has RLS enforced against it, which is the
    -- whole point.
    EXECUTE format('ALTER TABLE %I.users ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.user_identities ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.refresh_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.email_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.vault_secrets ENABLE ROW LEVEL SECURITY', v_schema_name);

    -- Policy shape is identical to shared cluster (migration
    -- 000063 + 000055/000164 sensitive-table tightening).
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

    -- Per-tenant auth_* helpers (identical shape to shared cluster).
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

    -- Grants to the runtime role. Matches the shared cluster's
    -- eurobase_gateway grants at the end of provision_tenant, minus
    -- the ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator clause
    -- (no _ddl role here in part 2b).
    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    -- Any future object created in this schema by the owner is
    -- auto-granted to eurobase_gateway. Keeps SDK DDL (via the
    -- future migration executor) working without a follow-up
    -- provision_tenant pass.
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', v_schema_name);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO eurobase_gateway', v_schema_name);

    SET search_path TO public;
    RETURN v_schema_name;
END;
$fn$;
