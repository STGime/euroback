-- 000008_platform_auth.up.sql
-- Add auth columns to platform_users, add jwt_secret to projects,
-- create platform_config table for platform-level JWT secret.

BEGIN;

-- Ensure pgcrypto extension is available for gen_random_bytes().
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- 1. Add auth columns to platform_users
ALTER TABLE public.platform_users
    ADD COLUMN IF NOT EXISTS password_hash TEXT,
    ADD COLUMN IF NOT EXISTS email_confirmed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_sign_in_at TIMESTAMPTZ;

-- Drop hanko_user_id — no longer needed, email is the primary identity.
ALTER TABLE public.platform_users DROP CONSTRAINT IF EXISTS platform_users_hanko_user_id_key;
ALTER TABLE public.platform_users DROP COLUMN IF EXISTS hanko_user_id;

-- Add unique index on email for platform_users (if not already unique)
CREATE UNIQUE INDEX IF NOT EXISTS idx_platform_users_email ON public.platform_users(email)
    WHERE email != '';

-- 2. Add per-project JWT secret for end-user auth
ALTER TABLE public.projects
    ADD COLUMN IF NOT EXISTS jwt_secret TEXT;

UPDATE public.projects SET jwt_secret = encode(gen_random_bytes(32), 'hex') WHERE jwt_secret IS NULL;
ALTER TABLE public.projects ALTER COLUMN jwt_secret SET NOT NULL;
ALTER TABLE public.projects ALTER COLUMN jwt_secret SET DEFAULT encode(gen_random_bytes(32), 'hex');

-- 3. Platform config table (stores platform-level secrets as key-value rows)
CREATE TABLE IF NOT EXISTS public.platform_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Insert a default platform JWT secret (should be overridden by PLATFORM_JWT_SECRET env var)
INSERT INTO public.platform_config (key, value)
VALUES ('jwt_secret', encode(gen_random_bytes(32), 'hex'))
ON CONFLICT (key) DO NOTHING;

COMMIT;
