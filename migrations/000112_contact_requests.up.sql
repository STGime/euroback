-- 000112_contact_requests.up.sql
--
-- Public marketing-site contact form (eurobase.app "Contact" widget).
-- Rows are inserted by the unauthenticated `POST /platform/public/contact`
-- handler; a superadmin later triages via /admin/contact-requests (UI
-- lands in a follow-up).
--
-- Privacy shape: email + optional name + message body live here until a
-- superadmin resolves them. Storing IP + user_agent is deliberate — helps
-- correlate spam waves and (rare) support cases where a returning visitor
-- follows up. Both are personal data under GDPR; retention gets a
-- 12-month sweep in a follow-up worker (out of scope here — inserting
-- rows first, retention policy second).
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

CREATE TABLE public.contact_requests (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NULL,
    email           TEXT NOT NULL,
    message         TEXT NOT NULL,
    -- Where the submission came from. Defaults to 'marketing_site' for
    -- the eurobase.app widget; leaving the column open so a future
    -- /console contact form, a docs feedback widget, or an SDK-embedded
    -- widget can distinguish themselves without a schema change.
    source          TEXT NOT NULL DEFAULT 'marketing_site',
    -- Client fingerprints kept for spam-correlation. INET is the native
    -- Postgres address type — no need to store as string.
    user_agent      TEXT NULL,
    ip_address      INET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Resolution bookkeeping. NULL = unresolved (visible on the admin
    -- triage view). resolved_by FK is nullable so a superadmin deletion
    -- doesn't cascade-delete the historic record.
    resolved_at     TIMESTAMPTZ NULL,
    resolved_by     UUID NULL REFERENCES public.platform_users(id) ON DELETE SET NULL,
    resolution_note TEXT NULL,

    -- Bounds match the handler's validation layer. Belt-and-braces so a
    -- bypassed handler (or a direct SQL insert by a compromised gateway
    -- role) still can't wedge the table with 10MB messages.
    CHECK (char_length(email)   BETWEEN 3 AND 254),
    CHECK (char_length(message) BETWEEN 1 AND 5000),
    CHECK (name IS NULL OR char_length(name) BETWEEN 1 AND 200),
    CHECK (source IN ('marketing_site', 'console', 'sdk', 'other'))
);

-- Admin-triage index — unresolved rows sorted by newest-first. Partial
-- so it stays tiny (resolved rows are the vast majority once ops catches
-- up; this index only tracks the inbox).
CREATE INDEX ix_contact_requests_unresolved_recent
    ON public.contact_requests (created_at DESC)
    WHERE resolved_at IS NULL;

-- Grants. Two distinct roles because the read+write surface differs by
-- caller:
--   - eurobase_gateway (SDK-runtime + public-endpoint pool) needs INSERT
--     only. The public contact handler runs under this pool.
--   - eurobase_developer (platform-authenticated console pool) needs
--     SELECT + UPDATE for the /admin/contact-requests triage view.
--
-- ── #443-class pitfall (billing_profiles precedent) ──
-- Migration 000037 sets ALTER DEFAULT PRIVILEGES FOR ROLE
-- eurobase_migrator IN SCHEMA public GRANT SELECT, INSERT, UPDATE,
-- DELETE ON TABLES TO eurobase_gateway. That means any new
-- migrator-owned public.* table auto-grants gateway all four DML
-- privileges — a naked "GRANT INSERT" here is redundant, and the
-- "gateway can't exfiltrate" property this table's design depends on
-- would not hold. The billing_profiles migration (000106) documents
-- the same trap; the fix is REVOKE ALL then GRANT the narrow surface.
REVOKE ALL ON public.contact_requests FROM eurobase_gateway;
GRANT INSERT ON public.contact_requests TO eurobase_gateway;
GRANT SELECT, UPDATE ON public.contact_requests TO eurobase_developer;

-- Note: the handler's INSERT ... RETURNING id needs SELECT on the
-- id column too, which migration 000113 grants (column-level) so
-- the PII columns stay unreadable to the gateway pool.

COMMENT ON TABLE public.contact_requests IS
  'Public marketing-site contact form submissions. Rate-limited per IP + email in the handler. Retention sweep TBD.';
COMMENT ON COLUMN public.contact_requests.source IS
  'Where the submission originated (marketing_site / console / sdk / other). Enables per-surface analytics without a schema change.';
COMMENT ON COLUMN public.contact_requests.resolved_at IS
  'NULL = unresolved (visible in admin triage). Stamped when a superadmin marks the request handled.';
