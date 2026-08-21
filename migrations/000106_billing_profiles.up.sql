-- 000106_billing_profiles.up.sql
--
-- Captures the buyer identity/address that Estonian VAT Act §37
-- and the Accounting Act require on invoices. Today's invoice
-- PDF (internal/billing/pdf.go) prints only the owner's email +
-- display_name, which fails B2B invoice-compliance checks and
-- gets bounced by any real accountant. Must ship BEFORE the
-- MOLLIE_ENV=live cutover so the first live invoice is compliant.
--
-- Design:
--
--  * Separate table (1:1 with platform_users via UNIQUE FK), NOT
--    columns on platform_users. Reasons:
--     - platform_users is auth-hot (every request touches it);
--       billing fields are cold and only accessed on checkout /
--       invoice render. Keep the hot row narrow.
--     - Presence/absence of a billing_profiles row is itself the
--       "needs to fill form" gate — no nullable-columns-are-they-
--       really-empty ambiguity.
--     - Clean surface for a future per-invoice snapshot table.
--
--  * NO RLS. Same isolation model as other public.* platform
--    tables: eurobase_developer (platform pool) has DML, the
--    runtime eurobase_gateway role gets no grant at all — the
--    SDK gateway must not see billing PII.
--
--  * ON DELETE CASCADE on platform_user_id: covers the GDPR
--    account-deletion path without an extra sweeper. If we ever
--    add a DSAR export path that includes billing profiles, the
--    row is still here to be exported before deletion.
--
--  * VAT number is regex-checked but NOT VIES-validated at this
--    layer. VIES is flaky (frequent per-member-state 5xx) and
--    we don't need reverse-charge logic until we cross the €40k
--    Estonian VAT threshold. Store what the user types.
--
--  * Registry code is optional at the DB level. The handler
--    conditionally requires it based on (country=EE, entity=
--    business) — country-specific rules belong in code, not in
--    a CHECK constraint that'd need a migration to relax later.
--
--  * updated_at is maintained by a per-table trigger. There's no
--    shared touch_updated_at() function in the schema yet
--    (000071 and 000083 both defined their own); follow the same
--    pattern so this migration doesn't create cross-file coupling.
--
--  * The 2 comped beta users (PR #442) do NOT need a backfill —
--    they will not be invoiced during the comp window. They'll
--    hit the form organically if/when they add a real payment.

BEGIN;

CREATE TABLE IF NOT EXISTS public.billing_profiles (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    platform_user_id  UUID        NOT NULL UNIQUE
                                  REFERENCES public.platform_users(id) ON DELETE CASCADE,
    entity_type       TEXT        NOT NULL
                                  CHECK (entity_type IN ('individual', 'business')),
    legal_name        TEXT        NOT NULL
                                  CHECK (length(legal_name) BETWEEN 2 AND 200),
    street_address    TEXT        NOT NULL
                                  CHECK (length(street_address) BETWEEN 2 AND 200),
    postal_code       TEXT        NOT NULL
                                  CHECK (length(postal_code) BETWEEN 1 AND 20),
    city              TEXT        NOT NULL
                                  CHECK (length(city) BETWEEN 1 AND 100),
    country           CHAR(2)     NOT NULL
                                  CHECK (country ~ '^[A-Z]{2}$'),
    registry_code     TEXT        NULL
                                  CHECK (registry_code IS NULL OR length(registry_code) BETWEEN 2 AND 40),
    vat_number        TEXT        NULL
                                  CHECK (vat_number IS NULL OR vat_number ~ '^[A-Z]{2}[A-Z0-9]{2,12}$'),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Trigger for updated_at. Per-table function following the
-- 000071 / 000083 pattern (no shared touch fn in this schema).
CREATE OR REPLACE FUNCTION public.billing_profiles_touch_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Lockdown per CLAUDE.md convention: every new SECURITY DEFINER
-- helper in public.* revokes PUBLIC EXECUTE in its own migration.
-- This function is SECURITY INVOKER (default), not DEFINER, so
-- the PUBLIC-EXECUTE-by-default risk doesn't apply — but the
-- REVOKE keeps the surface tidy and matches the pattern.
REVOKE EXECUTE ON FUNCTION public.billing_profiles_touch_updated_at() FROM PUBLIC;

DROP TRIGGER IF EXISTS trg_billing_profiles_touch_updated_at
    ON public.billing_profiles;

CREATE TRIGGER trg_billing_profiles_touch_updated_at
    BEFORE UPDATE ON public.billing_profiles
    FOR EACH ROW EXECUTE FUNCTION public.billing_profiles_touch_updated_at();

-- Grants: developer pool (platform + MCP traffic) needs full DML;
-- runtime SDK pool (eurobase_gateway) gets NOTHING — billing PII
-- must not leak through /v1/*. Migrator owns by default.
GRANT SELECT, INSERT, UPDATE, DELETE ON public.billing_profiles TO eurobase_developer;

COMMIT;
