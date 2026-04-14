-- 000032_user_identities_phone_auth.down.sql
-- Revert user_identities table, phone columns, and phone_verification token type.

BEGIN;

DO $$
DECLARE
    rec RECORD;
    con_name TEXT;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        -- Drop user_identities table.
        EXECUTE format('DROP TABLE IF EXISTS %I.user_identities', rec.schema_name);

        -- Remove phone columns.
        EXECUTE format('ALTER TABLE %I.users DROP COLUMN IF EXISTS phone', rec.schema_name);
        EXECUTE format('ALTER TABLE %I.users DROP COLUMN IF EXISTS phone_confirmed_at', rec.schema_name);

        -- Revert email_tokens CHECK to remove phone_verification.
        SELECT c.conname INTO con_name
        FROM pg_constraint c
        JOIN pg_class r ON c.conrelid = r.oid
        JOIN pg_namespace n ON r.relnamespace = n.oid
        WHERE n.nspname = rec.schema_name
          AND r.relname = 'email_tokens'
          AND c.contype = 'c'
          AND pg_get_constraintdef(c.oid) LIKE '%token_type%';

        IF con_name IS NOT NULL THEN
            EXECUTE format(
                'ALTER TABLE %I.email_tokens DROP CONSTRAINT %I',
                rec.schema_name, con_name
            );
            EXECUTE format(
                'ALTER TABLE %I.email_tokens ADD CONSTRAINT %I CHECK (token_type IN (''verification'',''password_reset'',''magic_link''))',
                rec.schema_name, con_name
            );
        END IF;
    END LOOP;
END;
$$;

COMMIT;
