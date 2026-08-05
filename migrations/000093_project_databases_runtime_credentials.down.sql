BEGIN;

ALTER TABLE public.project_databases
    DROP CONSTRAINT IF EXISTS project_databases_runtime_all_or_none;

ALTER TABLE public.project_databases
    DROP COLUMN IF EXISTS runtime_username,
    DROP COLUMN IF EXISTS runtime_password_ciphertext,
    DROP COLUMN IF EXISTS runtime_password_nonce,
    DROP COLUMN IF EXISTS runtime_password_key_version;

COMMIT;
