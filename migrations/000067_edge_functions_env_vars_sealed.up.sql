-- 000067_edge_functions_env_vars_sealed.up.sql
--
-- Close #206: `public.edge_functions.env_vars JSONB` was plaintext at rest,
-- contradicting the documented "encrypted at rest (vault key)" contract.
-- Anyone with SELECT on the table — service role inside the runner pool,
-- migrator, anyone reading a logical backup — saw raw env_vars including
-- whatever API keys developers naturally put there.
--
-- Strategy: introduce three columns that mirror the `vault_secrets` row
-- shape (AES-256-GCM ciphertext + per-row nonce + the key version it was
-- sealed with). Application code seals on Create/Update using the per-
-- tenant HKDF-derived key (internal/vault/keyprovider.go) and stores the
-- trio here; reads prefer the sealed columns and fall back to the legacy
-- plaintext JSONB so deployed-but-not-yet-updated rows keep running.
--
-- LAZY MIGRATION (no backfill in this file):
-- A backfill would have to derive the per-tenant key, which requires the
-- VAULT_ENCRYPTION_KEY secret — not available to the migrator role at
-- migrate-job time. Instead, every Update to an edge function lazy-seals
-- whatever values it has, then NULLs the legacy column. New writes go
-- straight to the sealed columns. The legacy column is dropped in a
-- follow-up migration once monitoring shows it's NULL everywhere.
--
-- Runtime contract (gateway service + functions-runner):
-- * If env_vars_blob IS NOT NULL → decrypt with key version
--   env_vars_key_version, AAD = nil (matches vault_secrets).
-- * Else if env_vars IS NOT NULL → use as-is (legacy plaintext).
-- * Else → empty map.

ALTER TABLE public.edge_functions
    ADD COLUMN env_vars_blob        BYTEA,
    ADD COLUMN env_vars_nonce       BYTEA,
    ADD COLUMN env_vars_key_version SMALLINT;

-- Either all three sealed columns are populated or none — guards against a
-- partial write that would leave a row undecryptable. The legacy
-- env_vars column is independent and intentionally not part of the check.
ALTER TABLE public.edge_functions
    ADD CONSTRAINT edge_functions_env_vars_sealed_consistent CHECK (
        (env_vars_blob IS NULL AND env_vars_nonce IS NULL AND env_vars_key_version IS NULL)
     OR (env_vars_blob IS NOT NULL AND env_vars_nonce IS NOT NULL AND env_vars_key_version IS NOT NULL)
    );

COMMENT ON COLUMN public.edge_functions.env_vars IS
    'DEPRECATED legacy plaintext env vars. Surviving rows are lazy-migrated to env_vars_blob/_nonce/_key_version on the next Update. Once monitoring shows this column is NULL everywhere, it can be dropped in a follow-up migration.';

COMMENT ON COLUMN public.edge_functions.env_vars_blob IS
    'AES-256-GCM ciphertext of JSON({"K":"V"}) sealed with the per-tenant HKDF-derived key. Decrypt with key version env_vars_key_version and IV env_vars_nonce. See #206.';
