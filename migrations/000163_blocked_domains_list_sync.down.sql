-- 000163_blocked_domains_list_sync.down.sql
DROP TABLE IF EXISTS public.platform_blocked_domain_sync;
DROP TABLE IF EXISTS public.platform_blocked_domain_exclusions;
ALTER TABLE public.platform_blocked_signup_domains
    DROP CONSTRAINT IF EXISTS platform_blocked_signup_domains_source_check;
ALTER TABLE public.platform_blocked_signup_domains DROP COLUMN IF EXISTS source;
