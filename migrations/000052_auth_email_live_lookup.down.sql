-- 000052_auth_email_live_lookup.down.sql
--
-- Revert auth.email() and per-tenant auth_email() back to reading the
-- app.end_user_email GUC. Policies that started depending on the live
-- lookup will silently revert to stale-JWT behaviour after this runs;
-- check call sites before using.
--
-- No explicit BEGIN/COMMIT — golang-migrate wraps the file.

CREATE OR REPLACE FUNCTION auth.email() RETURNS text
    LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.end_user_email', true), '')
$$;

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        EXECUTE format(
            'CREATE OR REPLACE FUNCTION %I.auth_email() RETURNS text
             LANGUAGE sql STABLE AS $_$
               SELECT current_setting(''app.end_user_email'', true);
             $_$', rec.schema_name
        );
    END LOOP;
END$$;
