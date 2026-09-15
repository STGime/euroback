-- 000118_platform_verification_token.up.sql
--
-- Platform (console) signup gains email verification. The
-- public.platform_email_tokens.token_type CHECK previously allowed only
-- ('password_reset','magic_link') (migration 000018); add 'verification'
-- so SendPlatformVerificationEmail can store a verification token for a
-- console user.
--
-- Idempotent: discovers the existing token_type CHECK by name (it was
-- created unnamed / auto-named) and swaps it for the expanded set, the
-- same pattern migration 000018 used.

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
      AND r.relname = 'platform_email_tokens'
      AND c.contype = 'c'
      AND pg_get_constraintdef(c.oid) LIKE '%token_type%';

    IF con_name IS NOT NULL THEN
        EXECUTE format(
            'ALTER TABLE public.platform_email_tokens DROP CONSTRAINT %I', con_name
        );
        EXECUTE format(
            'ALTER TABLE public.platform_email_tokens ADD CONSTRAINT %I CHECK (token_type IN (''password_reset'',''magic_link'',''verification''))',
            con_name
        );
    END IF;
END;
$$;

COMMIT;
