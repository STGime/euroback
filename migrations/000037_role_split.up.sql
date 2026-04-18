-- 000037_role_split.up.sql
--
-- Split runtime Postgres privileges.
--
--   eurobase_gateway   — runtime role used by the gateway + worker.
--                        DML only on public.*, no DDL. Owns its own
--                        tables in tenant schemas so the SDK DDL endpoint
--                        can create/drop tables there.
--   eurobase_migrator  — deploy-time role. Owns public.* tables and
--                        tenant schemas. Used by the migration Job in
--                        CI and nowhere else.
--   eurobase_api       — the original admin role. Kept alive as a
--                        rollback path; remove via the Scaleway console
--                        once the cutover is proven.
--
-- Both new roles MUST be created via the Scaleway console BEFORE this
-- migration runs. This file only does GRANT / REVOKE / ALTER OWNER.

BEGIN;

-- Fail fast if the roles are missing.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_migrator') THEN
        RAISE EXCEPTION 'role eurobase_migrator does not exist — create it via the Scaleway console first';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_gateway') THEN
        RAISE EXCEPTION 'role eurobase_gateway does not exist — create it via the Scaleway console first';
    END IF;
END$$;

-- Migrator needs membership in eurobase_api so REASSIGN OWNED works.
-- Running as a Scaleway admin role, which can grant any role to any role.
GRANT eurobase_api TO eurobase_migrator;

-- Transfer every object currently owned by eurobase_api to eurobase_migrator.
-- Covers public.* tables, sequences, functions, and every tenant_* schema
-- created by earlier runs of provision_tenant.
REASSIGN OWNED BY eurobase_api TO eurobase_migrator;

-- ============================================================
-- Gateway role: runtime DML, no DDL on public.*
-- ============================================================

GRANT CONNECT ON DATABASE eurobase TO eurobase_gateway;
GRANT USAGE ON SCHEMA public TO eurobase_gateway;

-- Existing platform tables: DML only.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO eurobase_gateway;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO eurobase_gateway;

-- Explicitly deny DDL on public. REVOKE CREATE FROM PUBLIC is belt-and-
-- braces against any new role accidentally inheriting CREATE.
REVOKE CREATE ON SCHEMA public FROM eurobase_gateway;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

-- Future platform tables created by the migrator automatically grant DML
-- to the gateway — no follow-up migration needed.
ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway;
ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO eurobase_gateway;

-- Gateway needs EXECUTE on provision_tenant. The function is SECURITY
-- DEFINER owned by migrator, so when the gateway calls it the schema
-- creation runs with migrator's privileges — gateway never needs CREATE
-- on the database.
GRANT EXECUTE ON FUNCTION public.provision_tenant(UUID, TEXT, TEXT) TO eurobase_gateway;

-- ============================================================
-- Existing tenant schemas: grant gateway DDL + DML on its own data.
-- ============================================================

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO eurobase_gateway', rec.schema_name);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO eurobase_gateway', rec.schema_name);
        EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO eurobase_gateway', rec.schema_name);
        -- Default privileges cover tables created by EITHER role in this schema going forward.
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', rec.schema_name);
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_gateway IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', rec.schema_name);
    END LOOP;
END$$;

-- ============================================================
-- provision_tenant: grant gateway access to each new tenant schema.
-- ============================================================
-- Body matches migration 000023 with five additional GRANT/ALTER DEFAULT
-- statements at the end (just before the UPDATE) that give the new
-- gateway role the same privileges we just applied retroactively.

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
            id                UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            email             TEXT        UNIQUE NOT NULL,
            password_hash     TEXT,
            display_name      TEXT,
            avatar_url        TEXT,
            metadata          JSONB       DEFAULT ''{}''::jsonb,
            provider          TEXT        DEFAULT ''email'',
            provider_user_id  TEXT,
            email_confirmed_at TIMESTAMPTZ,
            last_sign_in_at   TIMESTAMPTZ,
            banned_at         TIMESTAMPTZ,
            created_at        TIMESTAMPTZ DEFAULT now(),
            updated_at        TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );
    EXECUTE format('CREATE UNIQUE INDEX idx_users_provider ON %I.users(provider, provider_user_id) WHERE provider_user_id IS NOT NULL', v_schema_name);

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
            token_type  TEXT        NOT NULL CHECK (token_type IN (''verification'',''password_reset'')),
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
    EXECUTE format('ALTER TABLE %I.refresh_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.email_tokens ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.todos ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.vault_secrets ENABLE ROW LEVEL SECURITY', v_schema_name);

    EXECUTE format('CREATE POLICY user_self_access ON %I.users USING (id = current_setting(''app.end_user_id'', true)::uuid)', v_schema_name);
    EXECUTE format('CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY email_tokens_policy ON %I.email_tokens USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY storage_owner_access ON %I.storage_objects USING (uploaded_by = current_setting(''app.end_user_id'', true)::uuid)', v_schema_name);
    EXECUTE format('CREATE POLICY public_todos ON %I.todos FOR ALL USING (true)', v_schema_name);
    EXECUTE format('CREATE POLICY vault_secrets_policy ON %I.vault_secrets USING (true)', v_schema_name);

    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.auth_uid() RETURNS uuid
         LANGUAGE sql STABLE AS $_$
           SELECT current_setting(''app.end_user_id'', true)::uuid;
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

    -- NEW: grant the gateway runtime role access to this tenant schema.
    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO eurobase_gateway', v_schema_name);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', v_schema_name);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_gateway IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway', v_schema_name);

    UPDATE public.projects SET schema_name = v_schema_name WHERE id = p_project_id;
    SET search_path TO public;
END;
$fn$;

ALTER FUNCTION public.provision_tenant(UUID, TEXT, TEXT) OWNER TO eurobase_migrator;

COMMIT;
