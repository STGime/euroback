-- 000003_tenant_users_email_unique.down.sql
-- Remove unique constraint on email from all tenant users tables.

DO $$
DECLARE
    schema_rec RECORD;
BEGIN
    FOR schema_rec IN
        SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        EXECUTE format(
            'ALTER TABLE %I.users DROP CONSTRAINT IF EXISTS users_email_unique',
            schema_rec.schema_name
        );
    END LOOP;
END;
$$;
