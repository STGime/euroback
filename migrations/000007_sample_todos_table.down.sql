-- 000007_sample_todos_table.down.sql
-- Revert provision_tenant() to the version without the todos table.

BEGIN;

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
