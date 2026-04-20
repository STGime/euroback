-- 000038_rls_service_role.down.sql
-- Revert to the pre-000038 policy shape. Existing tenant schemas are
-- restored; provision_tenant stays on the 000038 body (rolling it back
-- requires re-running 000023's function body).

BEGIN;

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        -- Restore auth_uid() to the original (unsafe) form.
        EXECUTE format(
            'CREATE OR REPLACE FUNCTION %I.auth_uid() RETURNS uuid
             LANGUAGE sql STABLE AS $_$
               SELECT current_setting(''app.end_user_id'', true)::uuid;
             $_$', rec.schema_name
        );

        EXECUTE format('DROP POLICY IF EXISTS user_self_access ON %I.users', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY user_self_access ON %I.users
             USING (id = current_setting(''app.end_user_id'', true)::uuid)',
            rec.schema_name
        );

        EXECUTE format('DROP POLICY IF EXISTS storage_owner_access ON %I.storage_objects', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY storage_owner_access ON %I.storage_objects
             USING (uploaded_by = current_setting(''app.end_user_id'', true)::uuid)',
            rec.schema_name
        );
    END LOOP;
END$$;

DROP FUNCTION IF EXISTS public.is_service_role();
DROP FUNCTION IF EXISTS public.current_end_user_id();

COMMIT;
