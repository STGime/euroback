-- Revert 000118: drop 'verification' from the platform_email_tokens
-- token_type CHECK, restoring the 000018 set. Any 'verification' rows
-- must be cleared first or the new constraint fails to validate.

BEGIN;

DELETE FROM public.platform_email_tokens WHERE token_type = 'verification';

DO $$
DECLARE
    con_name TEXT;
BEGIN
    SELECT c.conname INTO con_name
    FROM pg_constraint c
    JOIN pg_class r ON c.conrelid = r.oid
    JOIN pg_namespace n ON r.relnamespace = n.oid
    WHERE n.nspname = 'public'
      AND r.relname = 'platform_email_tokens'
      AND c.contype = 'c'
      AND pg_get_constraintdef(c.oid) LIKE '%token_type%';

    IF con_name IS NOT NULL THEN
        EXECUTE format(
            'ALTER TABLE public.platform_email_tokens DROP CONSTRAINT %I', con_name
        );
        EXECUTE format(
            'ALTER TABLE public.platform_email_tokens ADD CONSTRAINT %I CHECK (token_type IN (''password_reset'',''magic_link''))',
            con_name
        );
    END IF;
END;
$$;

COMMIT;
