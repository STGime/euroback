-- 000163_blocked_domains_list_sync.up.sql
--
-- Keeps the signup blocklist in step with a public list of disposable
-- email domains (worker: workers.StartDisposableDomainSync, weekly).
--
--   * platform_blocked_signup_domains.source: 'manual' (an admin added it)
--     or 'disposable_list' (the sync added it). The sync only ever adds or
--     removes its own rows; manual rows are never touched.
--   * platform_blocked_domain_exclusions: domains an admin unblocked while
--     they came from the list (a false positive). The sync never re-adds
--     them. Blocking the domain by hand again removes the exclusion.
--   * platform_blocked_domain_sync: one row with the last run's outcome,
--     shown on Admin → Blocked domains.
--
-- Rows already loaded from the list (reason starting with "disposable
-- email (disposable-email-domains list") are marked 'disposable_list'.
-- Migrator-owned tables in public; 000037's default privileges give
-- eurobase_gateway DML (gateway admin handlers, worker), like 000153.
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

ALTER TABLE public.platform_blocked_signup_domains
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'manual';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conname = 'platform_blocked_signup_domains_source_check') THEN
        ALTER TABLE public.platform_blocked_signup_domains
            ADD CONSTRAINT platform_blocked_signup_domains_source_check
            CHECK (source IN ('manual', 'disposable_list'));
    END IF;
END $$;

UPDATE public.platform_blocked_signup_domains
   SET source = 'disposable_list'
 WHERE reason LIKE 'disposable email (disposable-email-domains list%';

CREATE TABLE IF NOT EXISTS public.platform_blocked_domain_exclusions (
    domain     TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.platform_blocked_domain_sync (
    id          INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_run_at TIMESTAMPTZ,
    last_ok_at  TIMESTAMPTZ,
    list_size   INT,
    added       INT,
    removed     INT,
    error       TEXT
);
INSERT INTO public.platform_blocked_domain_sync (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
