-- 000004_fix_tenant_rls_policies.up.sql
-- Fix broken RLS policies on tenant tables.
--
-- The old policies compared user UUIDs against app.tenant_id (a project UUID),
-- which never matched, causing all queries to return 0 rows.
-- Tenant isolation is already enforced by schema separation (tenant_<project_id>),
-- so the correct RLS policy is simply USING (true).

BEGIN;

-----------------------------------------------------------------
-- 1. Fix existing tenant schemas
-----------------------------------------------------------------
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT nspname AS schema_name
          FROM pg_namespace
         WHERE nspname LIKE 'tenant_%'
    LOOP
        -- Drop broken policies
        EXECUTE format(
            'DROP POLICY IF EXISTS tenant_isolation_users ON %I.users',
            r.schema_name
        );
        EXECUTE format(
            'DROP POLICY IF EXISTS tenant_isolation_storage ON %I.storage_objects',
            r.schema_name
        );

        -- Recreate with permissive USING (true)
        EXECUTE format(
            'CREATE POLICY tenant_isolation_users ON %I.users FOR ALL USING (true)',
            r.schema_name
        );
        EXECUTE format(
            'CREATE POLICY tenant_isolation_storage ON %I.storage_objects FOR ALL USING (true)',
            r.schema_name
        );
    END LOOP;
END;
$$;

-----------------------------------------------------------------
-- 2. Update provision_tenant() so new tenants get correct policies
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
    -- Derive schema name: tenant_<project_id with hyphens replaced by underscores>
    v_schema_name := 'tenant_' || replace(p_project_id::text, '-', '_');

    -- Create the tenant schema
    EXECUTE format('CREATE SCHEMA %I', v_schema_name);

    -- Set search_path to the new schema
    EXECUTE format('SET search_path TO %I', v_schema_name);

    -----------------------------------------------------------------
    -- users table
    -----------------------------------------------------------------
    EXECUTE format(
        'CREATE TABLE %I.users (
            id            UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
            hanko_user_id TEXT        UNIQUE,
            email         TEXT,
            display_name  TEXT,
            avatar_url    TEXT,
            metadata      JSONB       DEFAULT ''{}''::jsonb,
            created_at    TIMESTAMPTZ DEFAULT now(),
            updated_at    TIMESTAMPTZ DEFAULT now()
        )',
        v_schema_name
    );

    -----------------------------------------------------------------
    -- storage_objects table
    -----------------------------------------------------------------
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

    -----------------------------------------------------------------
    -- Enable RLS on both tables
    -----------------------------------------------------------------
    EXECUTE format('ALTER TABLE %I.users ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);

    -----------------------------------------------------------------
    -- RLS policies: permissive — schema separation is the isolation boundary
    -----------------------------------------------------------------
    EXECUTE format(
        'CREATE POLICY tenant_isolation_users ON %I.users FOR ALL USING (true)',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY tenant_isolation_storage ON %I.storage_objects FOR ALL USING (true)',
        v_schema_name
    );

    -----------------------------------------------------------------
    -- Update the projects table with the provisioned schema name
    -----------------------------------------------------------------
    UPDATE public.projects
       SET schema_name = v_schema_name
     WHERE id = p_project_id;

    -- Reset search_path
    SET search_path TO public;
END;
$$;

-----------------------------------------------------------------
-- 3. Drop set_tenant_id() — no longer needed
-----------------------------------------------------------------
DROP FUNCTION IF EXISTS public.set_tenant_id(UUID);

COMMIT;
