-- 000004_fix_tenant_rls_policies.down.sql
-- Revert to the old (broken) RLS policies and restore set_tenant_id().

BEGIN;

-----------------------------------------------------------------
-- 1. Restore set_tenant_id()
-----------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.set_tenant_id(p_tenant_id UUID)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM set_config('app.tenant_id', p_tenant_id::text, true);
END;
$$;

-----------------------------------------------------------------
-- 2. Revert existing tenant schemas to old policies
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
        EXECUTE format(
            'DROP POLICY IF EXISTS tenant_isolation_users ON %I.users',
            r.schema_name
        );
        EXECUTE format(
            'DROP POLICY IF EXISTS tenant_isolation_storage ON %I.storage_objects',
            r.schema_name
        );

        EXECUTE format(
            'CREATE POLICY tenant_isolation_users ON %I.users
                USING (id = current_setting(''app.tenant_id'', true)::uuid)',
            r.schema_name
        );
        EXECUTE format(
            'CREATE POLICY tenant_isolation_storage ON %I.storage_objects
                USING (uploaded_by = current_setting(''app.tenant_id'', true)::uuid)',
            r.schema_name
        );
    END LOOP;
END;
$$;

-----------------------------------------------------------------
-- 3. Restore original provision_tenant()
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
    v_schema_name := 'tenant_' || replace(p_project_id::text, '-', '_');

    EXECUTE format('CREATE SCHEMA %I', v_schema_name);
    EXECUTE format('SET search_path TO %I', v_schema_name);

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

    EXECUTE format('ALTER TABLE %I.users ENABLE ROW LEVEL SECURITY', v_schema_name);
    EXECUTE format('ALTER TABLE %I.storage_objects ENABLE ROW LEVEL SECURITY', v_schema_name);

    EXECUTE format(
        'CREATE POLICY tenant_isolation_users ON %I.users
            USING (id = current_setting(''app.tenant_id'', true)::uuid)',
        v_schema_name
    );
    EXECUTE format(
        'CREATE POLICY tenant_isolation_storage ON %I.storage_objects
            USING (uploaded_by = current_setting(''app.tenant_id'', true)::uuid)',
        v_schema_name
    );

    UPDATE public.projects
       SET schema_name = v_schema_name
     WHERE id = p_project_id;

    SET search_path TO public;
END;
$$;

COMMIT;
