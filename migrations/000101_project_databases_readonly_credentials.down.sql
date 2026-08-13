-- 000101_project_databases_readonly_credentials.down.sql
--
-- Drop the readonly credential columns + constraint. Data loss (any
-- populated ciphertext is discarded), which is acceptable: the
-- readonly role is re-derivable via a fresh bootstrap pass.

BEGIN;

ALTER TABLE public.project_databases
    DROP CONSTRAINT IF EXISTS project_databases_readonly_all_or_none;

ALTER TABLE public.project_databases
    DROP COLUMN IF EXISTS readonly_username,
    DROP COLUMN IF EXISTS readonly_password_ciphertext,
    DROP COLUMN IF EXISTS readonly_password_nonce,
    DROP COLUMN IF EXISTS readonly_password_key_version;

COMMIT;
