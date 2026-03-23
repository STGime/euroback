-- 000011_users_banned_at.down.sql
BEGIN;

DO $$
DECLARE
    v_schema TEXT;
BEGIN
    FOR v_schema IN
        SELECT schema_name FROM public.projects WHERE status = 'active'
    LOOP
        EXECUTE format(
            'ALTER TABLE %I.users DROP COLUMN IF EXISTS banned_at', v_schema
        );
    END LOOP;
END;
$$;

COMMIT;
