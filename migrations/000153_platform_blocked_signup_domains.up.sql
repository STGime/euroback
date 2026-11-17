-- 000153_platform_blocked_signup_domains.up.sql
--
-- Admin-managed signup domain blocklist. Deliverable domains we refuse at
-- platform signup because they only ever produce abuse / throwaway
-- accounts — distinct from mailaddr.IsReserved, which covers undeliverable
-- reserved/test TLDs. Enforcement is in Go: PlatformAuthService.SignUp
-- checks it right after the reserved-domain check. Managed from the admin
-- console via /platform/admin/blocked-domains (superadmin).
--
-- Store registrable domains (or subdomains), lowercase, no leading dot.
-- SignUp also refuses an address whose domain is a SUBDOMAIN of a listed
-- one (a row for 'fldubai.com' blocks 'x@mail.fldubai.com' too), so we
-- don't need a row per subdomain.
--
-- Migrator-owned table in public: migration 000037's ALTER DEFAULT
-- PRIVILEGES auto-grants eurobase_gateway full table DML (same as
-- platform_allowlist), so the gateway-pool signup read and the admin
-- writes work with no explicit grant. The list is just domain names, not
-- sensitive config, so there is no REVOKE like the org tables. No explicit
-- BEGIN/COMMIT: golang-migrate wraps the file.

CREATE TABLE IF NOT EXISTS public.platform_blocked_signup_domains (
    domain     TEXT PRIMARY KEY,
    reason     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed the first reported abuse domain.
INSERT INTO public.platform_blocked_signup_domains (domain, reason)
VALUES ('fldubai.com', 'abuse / throwaway signups')
ON CONFLICT (domain) DO NOTHING;
