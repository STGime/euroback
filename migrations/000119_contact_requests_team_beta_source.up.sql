-- 000119_contact_requests_team_beta_source.up.sql
--
-- Adds 'team_beta_request' to the contact_requests.source CHECK so the
-- authenticated POST /platform/team-beta-request/ handler (#589) can
-- distinguish these rows from the marketing-site widget's submissions
-- in the admin panel + Discord routing.
--
-- The 000112 CHECK was created anonymously, so discover its name and
-- rewrite in-place — same pattern the platform_email_tokens rewrite
-- in 000118 used.

BEGIN;

DO $$
DECLARE
    con_name TEXT;
BEGIN
    SELECT c.conname INTO con_name
      FROM pg_constraint c
      JOIN pg_class r ON c.conrelid = r.oid
      JOIN pg_namespace n ON r.relnamespace = n.oid
     WHERE n.nspname = 'public'
       AND r.relname = 'contact_requests'
       AND c.contype = 'c'
       AND pg_get_constraintdef(c.oid) LIKE '%source%';

    IF con_name IS NOT NULL THEN
        EXECUTE format(
            'ALTER TABLE public.contact_requests DROP CONSTRAINT %I', con_name
        );
        EXECUTE format(
            'ALTER TABLE public.contact_requests ADD CONSTRAINT %I CHECK (source IN (''marketing_site'',''console'',''sdk'',''other'',''team_beta_request''))',
            con_name
        );
    END IF;
END;
$$;

COMMIT;
