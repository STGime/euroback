-- 000067_edge_functions_env_vars_sealed.down.sql
--
-- Reverses 000067. Drops the three sealed columns and restores the legacy
-- env_vars column's comment to NULL. A roll-back never decrypts: any rows
-- that had only the sealed columns populated will lose env vars on
-- rollback, which is the only safe behaviour for a one-way encryption
-- column being removed. Runtime code must be deployed before this is run.

ALTER TABLE public.edge_functions
    DROP CONSTRAINT IF EXISTS edge_functions_env_vars_sealed_consistent;

ALTER TABLE public.edge_functions
    DROP COLUMN IF EXISTS env_vars_blob,
    DROP COLUMN IF EXISTS env_vars_nonce,
    DROP COLUMN IF EXISTS env_vars_key_version;

COMMENT ON COLUMN public.edge_functions.env_vars IS NULL;
