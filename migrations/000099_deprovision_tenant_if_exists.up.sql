-- 000099_deprovision_tenant_if_exists.up.sql
--
-- Make deprovision_tenant tolerant of a missing platform-DB schema.
--
-- Team-tier projects live on a dedicated Scaleway managed-PG instance,
-- so their tenant schema is never created on the platform DB. When a
-- user deletes such a project the platform-side DROP SCHEMA hits
-- SQLSTATE 3F000 ("schema does not exist") and the whole delete flow
-- 500s AFTER project_databases rows have already been hard-deleted —
-- leaving the projects row orphaned (see gateway log 2026-08-12
-- 19:58:31 for project fe9fd098-c923-4ce9-a4e5-95af47f68a4a).
--
-- The function's contract is "make sure the tenant schema no longer
-- exists on this database". If it already doesn't, that's success.
-- Switch DROP SCHEMA to IF EXISTS so Team-tier deletes complete and
-- Free/Pro deletes still drop the schema normally.

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

    EXECUTE format('DROP SCHEMA IF EXISTS %I CASCADE', v_schema_name);
END;
$$;

COMMIT;
