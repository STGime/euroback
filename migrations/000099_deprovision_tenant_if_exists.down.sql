-- 000099_deprovision_tenant_if_exists.down.sql
--
-- Revert deprovision_tenant to the pre-000099 body (no IF EXISTS).

BEGIN;

CREATE OR REPLACE FUNCTION public.deprovision_tenant(p_project_id UUID)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    v_schema_name TEXT;
BEGIN
    SELECT schema_name INTO v_schema_name
      FROM public.projects
     WHERE id = p_project_id;

    IF v_schema_name IS NULL THEN
        RAISE EXCEPTION 'No schema found for project %', p_project_id;
    END IF;

    EXECUTE format('DROP SCHEMA %I CASCADE', v_schema_name);
END;
$$;

COMMIT;
