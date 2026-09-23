-- Reverse of 000123_platform_passkey_credentials.up.sql.
REVOKE SELECT (id, email), UPDATE (password_hash) ON public.platform_users FROM eurobase_developer;
DROP TABLE IF EXISTS public.platform_webauthn_challenges;
DROP TABLE IF EXISTS public.platform_passkey_credentials;
