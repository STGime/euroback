-- 000002_tenant_functions.up.sql
-- Tenant isolation functions: set_tenant_id, provision_tenant, deprovision_tenant.

BEGIN;

-- 1. set_tenant_id: sets session-level tenant context for RLS policies.
CREATE OR REPLACE FUNCTION public.set_tenant_id(p_tenant_id UUID)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM set_config('app.tenant_id', p_tenant_id::text, true);
END;
$$;

-- 2. provision_tenant: creates an isolated schema with default tables and RLS.
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
    -- RLS policies: enforce tenant isolation via app.tenant_id
    -----------------------------------------------------------------
    -- users: allow access only when the session tenant_id matches the user's id
    EXECUTE format(
        'CREATE POLICY tenant_isolation_users ON %I.users
            USING (id = current_setting(''app.tenant_id'', true)::uuid)',
        v_schema_name
    );

    -- storage_objects: allow access only when uploaded_by matches the session tenant_id
    EXECUTE format(
        'CREATE POLICY tenant_isolation_storage ON %I.storage_objects
            USING (uploaded_by = current_setting(''app.tenant_id'', true)::uuid)',
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

-- 3. deprovision_tenant: drops the tenant schema and all its objects.
CREATE OR REPLACE FUNCTION public.deprovision_tenant(p_project_id UUID)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    v_schema_name TEXT;
BEGIN
    -- Look up the schema name from the projects table
    SELECT schema_name INTO v_schema_name
      FROM public.projects
     WHERE id = p_project_id;

    IF v_schema_name IS NULL THEN
        RAISE EXCEPTION 'No schema found for project %', p_project_id;
    END IF;

    -- Drop the schema and all contained objects
    EXECUTE format('DROP SCHEMA %I CASCADE', v_schema_name);
END;
$$;

COMMIT;
