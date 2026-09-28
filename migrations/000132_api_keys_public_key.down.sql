ALTER TABLE public.api_keys DROP CONSTRAINT IF EXISTS api_keys_public_key_public_only;
ALTER TABLE public.api_keys DROP COLUMN IF EXISTS public_key;
