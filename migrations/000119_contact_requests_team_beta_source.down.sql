-- Revert 000119. Delete any rows with the new source value first so the
-- narrower CHECK doesn't fail to validate.

BEGIN;

DELETE FROM public.contact_requests WHERE source = 'team_beta_request';

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
            'ALTER TABLE public.contact_requests ADD CONSTRAINT %I CHECK (source IN (''marketing_site'',''console'',''sdk'',''other''))',
            con_name
        );
    END IF;
END;
$$;

COMMIT;
