-- 000132_api_keys_public_key.up.sql
--
-- The project's PUBLIC key (eb_pk_…) is public by design — it ships in
-- every browser app — so the console can show it again instead of only at
-- creation (Connect page, Lovable onboarding). Stored in plaintext for
-- public-type rows only; secret keys stay hash-only (key_hash), enforced by
-- the CHECK below.
--
-- Existing rows start NULL: the gateway's API-key middleware fills a
-- public row's column the first time that key is used (it sees the
-- plaintext on every SDK request), so active projects backfill themselves.
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

ALTER TABLE public.api_keys ADD COLUMN IF NOT EXISTS public_key TEXT;

ALTER TABLE public.api_keys DROP CONSTRAINT IF EXISTS api_keys_public_key_public_only;
ALTER TABLE public.api_keys ADD CONSTRAINT api_keys_public_key_public_only
    CHECK (public_key IS NULL OR (type = 'public' AND public_key LIKE 'eb\_pk\_%'));
